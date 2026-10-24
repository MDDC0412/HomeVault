package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"homevault/internal/audit"
	"homevault/internal/auth"
	"homevault/internal/core"
	"homevault/internal/state"
)

// retrySeconds 把剩余锁定时间转成 Retry-After 需要的秒数字符串（向上取整，至少 1）。
func retrySeconds(d time.Duration) string {
	sec := int(d / time.Second)
	if d%time.Second > 0 {
		sec++
	}
	if sec < 1 {
		sec = 1
	}
	return strconv.Itoa(sec)
}

// 用户/管理员对外表示的账号信息（绝不包含密码散列）。
type userView struct {
	Username              string    `json:"username"`
	DisplayName           string    `json:"displayName,omitempty"`
	IsAdmin               bool      `json:"isAdmin"`
	Root                  string    `json:"root,omitempty"`
	Quota                 int64     `json:"quota"`
	RateLimitMbps         int       `json:"rateLimitMbps"`
	Disabled              bool      `json:"disabled"`
	Permissions           []string  `json:"permissions"`
	MustChangeCredentials bool      `json:"mustChangeCredentials"`
	Used                  int64     `json:"used,omitempty"`
	TrashUsed             int64     `json:"trashUsed,omitempty"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt,omitempty"`
}

// viewOf 把账号记录转换为对外结构；withUsage 为真时附带用量（会遍历目录，注意缓存）。
func (s *Server) viewOf(u *state.User, withUsage bool) userView {
	v := userView{
		Username: u.Username, DisplayName: u.DisplayName, IsAdmin: u.IsAdmin,
		Root: u.Root, Quota: u.Quota, RateLimitMbps: u.RateLimitMbps,
		Disabled: u.Disabled, Permissions: u.PermList(),
		MustChangeCredentials: u.MustChangeCredentials,
		CreatedAt:             u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
	if withUsage && u.Root != "" {
		if used, trash, err := s.svc.Usage(u); err == nil {
			v.Used, v.TrashUsed = used, trash
		}
	}
	return v
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": Version, "time": time.Now().UTC(),
	})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	ip := s.clientIP(r)
	act := core.Actor{Username: req.Username, IP: ip, UserAgent: r.Header.Get("User-Agent")}

	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请输入账号和密码")
		return
	}
	retry := s.limiter.RetryAfter(req.Username, ip)
	if !s.limiter.Allowed(req.Username, ip) {
		s.svc.Audit(act, audit.ActionLoginFailed, req.Username, audit.StatusDenied, 0,
			"登录尝试过多，已临时锁定 "+retry.Round(time.Second).String())
		w.Header().Set("Retry-After", retrySeconds(retry))
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts",
			"密码错误次数过多，请 "+retry.Round(time.Second).String()+" 后再试")
		return
	}
	user, ok := s.svc.State.User(req.Username)
	if !ok || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		s.limiter.Fail(req.Username, ip)
		s.svc.Audit(act, audit.ActionLoginFailed, req.Username, audit.StatusDenied, 0, "账号或密码错误")
		// 不区分"账号不存在"与"密码错误"，避免账号枚举
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "账号或密码错误")
		return
	}
	if user.Disabled {
		s.svc.Audit(act, audit.ActionLoginFailed, req.Username, audit.StatusDenied, 0, "账号已被禁用")
		writeErr(w, http.StatusForbidden, "disabled", "账号已被禁用，请联系管理员")
		return
	}
	s.limiter.Reset(req.Username, ip)
	s.setSession(w, user.Username, user.PasswordHash)
	s.svc.Audit(act, audit.ActionLogin, user.Username, audit.StatusOK, 0, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"user": s.viewOf(user, false),
		"home": homeFor(user),
	})
}

// homeFor 告诉前端该跳到用户端还是管理端。
func homeFor(u *state.User) string {
	if u.IsAdmin {
		return "/admin/"
	}
	return "/"
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if u := userFrom(r); u != nil {
		s.svc.Audit(s.actor(r), audit.ActionLogout, u.Username, audit.StatusOK, 0, "")
	}
	s.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	resp := map[string]any{
		"user":             s.viewOf(u, true),
		"home":             homeFor(u),
		"officeAvailable":  s.svc.OfficeAvailable(),
		"sessionTTL":       s.svc.Settings().SessionTTL,
		"spaceDir":         s.cfg.SpaceDir,
		"version":          Version,
		"permissionLabels": permissionLabelsFor(u),
	}
	writeJSON(w, http.StatusOK, resp)
}

type changeCredReq struct {
	CurrentPassword string `json:"currentPassword"`
	NewUsername     string `json:"newUsername"`
	NewPassword     string `json:"newPassword"`
	NewDisplayName  string `json:"newDisplayName"`
}

// handleChangeCredentials 允许账号自己修改账号名/密码（必须验证当前密码）。
func (s *Server) handleChangeCredentials(w http.ResponseWriter, r *http.Request) {
	var req changeCredReq
	if !decodeJSON(w, r, &req) {
		return
	}
	u := userFrom(r)
	ip := s.clientIP(r)
	// 与登录接口共用同一套限流：不能让"改凭据"变成无限猜当前密码的后门。
	if !s.limiter.Allowed(u.Username, ip) {
		retry := s.limiter.RetryAfter(u.Username, ip)
		s.svc.Audit(s.actor(r), audit.ActionUserUpdate, u.Username, audit.StatusDenied, 0,
			"修改凭据尝试过多，已临时锁定")
		w.Header().Set("Retry-After", retrySeconds(retry))
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts",
			"密码错误次数过多，请 "+retry.Round(time.Second).String()+" 后再试")
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, req.CurrentPassword) {
		s.limiter.Fail(u.Username, ip)
		s.svc.Audit(s.actor(r), audit.ActionUserUpdate, u.Username, audit.StatusDenied, 0, "当前密码不正确")
		writeErr(w, http.StatusForbidden, "invalid_password", "当前密码不正确")
		return
	}
	s.limiter.Reset(u.Username, ip)
	next := u.Clone()
	next.MustChangeCredentials = false
	renamed := false

	if name := strings.TrimSpace(req.NewUsername); name != "" && name != u.Username {
		if err := state.ValidateUsername(name); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_username", err.Error())
			return
		}
		if _, exists := s.svc.State.User(name); exists {
			writeErr(w, http.StatusConflict, "already_exists", "该账号已被占用")
			return
		}
		next.Username = name
		renamed = true
	}
	if req.NewPassword != "" {
		if err := checkPasswordStrength(req.NewPassword); err != nil {
			writeErr(w, http.StatusBadRequest, "weak_password", err.Error())
			return
		}
		if auth.VerifyPassword(u.PasswordHash, req.NewPassword) {
			writeErr(w, http.StatusBadRequest, "same_password", "新密码不能与当前密码相同")
			return
		}
		hash, err := auth.HashPassword(req.NewPassword)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", "密码加密失败")
			return
		}
		next.PasswordHash = hash
	}
	if dn := strings.TrimSpace(req.NewDisplayName); dn != "" {
		next.DisplayName = dn
	}
	if !renamed && next.PasswordHash == u.PasswordHash && next.DisplayName == u.DisplayName && !u.MustChangeCredentials {
		writeErr(w, http.StatusBadRequest, "no_change", "没有需要修改的内容")
		return
	}
	if renamed {
		if err := s.renameUser(u.Username, next); err != nil {
			s.writeCoreErr(w, err)
			return
		}
	} else if err := s.svc.State.PutUser(next); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_user", err.Error())
		return
	}
	s.setSession(w, next.Username, next.PasswordHash)
	s.svc.Audit(core.Actor{Username: next.Username, IP: s.clientIP(r), UserAgent: r.Header.Get("User-Agent")},
		audit.ActionUserUpdate, next.Username, audit.StatusOK, 0, "用户自行修改凭据")
	writeJSON(w, http.StatusOK, map[string]any{"user": s.viewOf(next, false), "home": homeFor(next)})
}

// renameUser 改名并接管其未完成的上传会话。
func (s *Server) renameUser(old string, next *state.User) error {
	if err := s.svc.State.PutUser(next); err != nil {
		return err
	}
	if err := s.svc.State.DeleteUser(old); err != nil {
		return err
	}
	for _, sess := range s.svc.State.UploadSessions() {
		if sess.Username == old {
			cp := *sess
			cp.Username = next.Username
			_ = s.svc.State.PutUploadSession(&cp)
		}
	}
	return nil
}

func checkPasswordStrength(pw string) error {
	if len([]rune(pw)) < 8 {
		return errText("密码至少 8 个字符")
	}
	if len([]rune(pw)) > 200 {
		return errText("密码过长")
	}
	return nil
}

type errText string

func (e errText) Error() string { return string(e) }

// handleSystem 返回运行状态；管理员额外拿到账号数等信息。
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	total, free, _ := DiskStats(s.cfg.SpaceDir)
	resp := map[string]any{
		"version":         Version,
		"uptimeSeconds":   int64(time.Since(s.started).Seconds()),
		"spaceDir":        s.cfg.SpaceDir,
		"diskTotal":       total,
		"diskFree":        free,
		"officeAvailable": s.svc.OfficeAvailable(),
		"isAdmin":         u.IsAdmin,
	}
	if u.IsAdmin {
		resp["userCount"] = len(s.svc.State.Users())
		resp["adminCount"] = s.svc.State.AdminCount()
		resp["trashCount"] = len(s.svc.State.TrashEntries())
		resp["uploadSessionCount"] = len(s.svc.State.UploadSessions())
		resp["configFile"] = s.cfg.Path
		resp["tlsEnabled"] = s.cfg.TLSEnabled()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDisks 列出本机可选的存储位置（仅管理员）。
func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := ListDisks()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// 物理盘概览失败不影响主流程：拿不到就返回空数组。
	devices, _ := ListBlockDevices()
	if devices == nil {
		devices = []BlockDevice{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"disks":           disks,
		"devices":         devices,
		"currentSpaceDir": s.cfg.SpaceDir,
		"defaultSpaceDir": defaultSpaceDirForAPI(),
	})
}

// handleBrowseDirs 列出某个目录下的子目录（仅管理员，用于挑存储位置）。
//
// 刻意只返回目录名：这个接口的用途是"选一个落盘位置"，不是文件管理器，
// 因此不返回文件名、大小或内容，也不跟随符号链接。
func (s *Server) handleBrowseDirs(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		raw = s.cfg.SpaceDir
	}
	if !filepath.IsAbs(raw) {
		writeErr(w, http.StatusBadRequest, "invalid_path", "目录必须是绝对路径")
		return
	}
	p := filepath.Clean(raw)
	ents, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_path", "无法读取该目录："+err.Error())
		return
	}
	type dirEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := make([]dirEntry, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// 隐藏目录（.git / .libreoffice / 快照目录）对"选存储位置"没有意义，跳过。
		if strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, dirEntry{Name: name, Path: filepath.Join(p, name)})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })

	parent := filepath.Dir(p)
	if parent == p {
		parent = "" // 已经在根目录
	}
	total, free, _ := DiskStats(p)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     p,
		"parent":   parent,
		"dirs":     dirs,
		"writable": writableDir(p),
		"total":    total,
		"free":     free,
		"hasState": fileExists(filepath.Join(p, "state.json")),
		"current":  p == s.cfg.SpaceDir,
	})
}

// ---------------------------------------------------------------------------
// 静态资源
// ---------------------------------------------------------------------------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Path
	if strings.HasPrefix(raw, "/api/") {
		writeErr(w, http.StatusNotFound, "not_found", "接口不存在")
		return
	}
	// 管理端与用户端分离：/admin 走管理端入口，其余走用户端。
	// 注意：必须看原始路径是否以 / 结尾，否则 / 与 /admin/ 会被反复重定向。
	if raw == "/admin" {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
		return
	}
	p := path.Clean(raw)
	admin := strings.HasPrefix(p, "/admin/") || p == "/admin"
	name := strings.TrimPrefix(p, "/")
	if admin {
		name = strings.TrimPrefix(p, "/admin/")
	}
	if name == "" || name == "." || name == "/" {
		name = "index.html"
	}
	if admin {
		// 静态文件可以在 /admin/ 下取，也可以回退到根目录的共享资源
		if name == "index.html" {
			name = "admin.html"
		} else if _, err := fs.Stat(s.static, name); err != nil {
			name = "admin.html"
		}
	}
	if data, err := fs.ReadFile(s.static, name); err == nil {
		w.Header().Set("Content-Type", contentTypeByName(name))
		// 前端资源是内嵌在二进制里的：升级后必须让浏览器重新取，
		// 所以一律 no-cache（仍会走 304 协商，不会重复下载未变的文件）。
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
		return
	}
	// SPA 兜底
	entry := "index.html"
	if admin {
		entry = "admin.html"
	}
	data, err := fs.ReadFile(s.static, entry)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "页面不存在")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func contentTypeByName(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
