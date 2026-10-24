package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homevault/internal/audit"
	"homevault/internal/auth"
	"homevault/internal/config"
	"homevault/internal/fsops"
	"homevault/internal/state"
)

func defaultSpaceDirForAPI() string { return config.DefaultSpaceDir() }

// ---------------------------------------------------------------------------
// 回收站
// ---------------------------------------------------------------------------

type trashView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	OriginalPath string     `json:"originalPath"`
	Root         string     `json:"root,omitempty"`
	Size         int64      `json:"size"`
	IsDir        bool       `json:"isDir"`
	DeletedBy    string     `json:"deletedBy"`
	DeletedAt    time.Time  `json:"deletedAt"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	// 权限看"操作者"，数据看"目标账号"：管理员代操作时以自己的权限放行，
	// 但列表仍按目标账号的根目录过滤。
	if !userFrom(r).Can(state.PermTrashView) {
		writeErr(w, http.StatusForbidden, "forbidden", "没有查看回收站的权限")
		return
	}
	u := targetFrom(r)
	entries := s.svc.VisibleTrash(u)
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	items := make([]trashView, 0, len(entries))
	var totalBytes int64
	for i, e := range entries {
		totalBytes += e.Size
		if i < offset || len(items) >= limit {
			continue
		}
		items = append(items, trashView{
			ID: e.ID, Name: e.Name, OriginalPath: e.OriginalPath, Root: e.Root,
			Size: e.Size, IsDir: e.IsDir, DeletedBy: e.DeletedBy,
			DeletedAt: e.DeletedAt, ExpiresAt: e.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": len(entries), "totalBytes": totalBytes,
	})
}

type trashReq struct {
	IDs      []string `json:"ids"`
	Password string   `json:"password"`
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	var req trashReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	restored, failed, err := s.svc.Restore(s.actor(r), targetFrom(r), req.IDs)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored": restored, "failed": failed})
}

func (s *Server) handleTrashPurge(w http.ResponseWriter, r *http.Request) {
	var req trashReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	deleted, err := s.svc.Purge(s.actor(r), targetFrom(r), req.IDs)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (s *Server) handleTrashEmpty(w http.ResponseWriter, r *http.Request) {
	var req trashReq
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	count, bytes, err := s.svc.EmptyTrash(s.actor(r), targetFrom(r))
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": count, "bytes": bytes})
}

// ---------------------------------------------------------------------------
// 审计日志
// ---------------------------------------------------------------------------

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	u := targetFrom(r)
	q := audit.Query{
		Limit:  queryInt(r, "limit", 100),
		Offset: queryInt(r, "offset", 0),
		Action: r.URL.Query().Get("action"),
		Search: r.URL.Query().Get("search"),
	}
	if u.IsAdmin {
		q.User = r.URL.Query().Get("user")
	} else {
		q.User = u.Username // 普通用户只能看自己的记录
	}
	if from := r.URL.Query().Get("from"); from != "" {
		t, err := parseDayOrTime(from)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "from 时间格式不正确")
			return
		}
		q.From = t
	}
	if to := r.URL.Query().Get("to"); to != "" {
		t, err := parseDayOrTime(to)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "to 时间格式不正确")
			return
		}
		q.To = t
	}
	res, err := s.svc.Logs.Query(q)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cd := "attachment; filename=\"homevault-logs.csv\""
		w.Header().Set("Content-Disposition", cd)
		if err := audit.WriteCSV(w, res.Entries); err != nil {
			s.logger.Error("导出 CSV 失败", "err", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": res.Entries, "total": res.Total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

// ---------------------------------------------------------------------------
// 日志清除：只能删 2 个月以前的记录，且必须再输一次管理员密码
// ---------------------------------------------------------------------------

// logMinRetention 是"清除日志"允许删到的最晚边界：
// 距今 2 个月以内的记录受保护，不允许删除。
const logMinRetention = 60 * 24 * time.Hour

// logPurgeCutoff 返回当前允许删除的时间边界（UTC）。
func logPurgeCutoff() time.Time {
	return time.Now().UTC().Add(-logMinRetention)
}

// handleLogsPurgeInfo 查询"如果现在清理会删掉多少"，用于前端确认弹窗。
func (s *Server) handleLogsPurgeInfo(w http.ResponseWriter, r *http.Request) {
	cutoff := logPurgeCutoff()
	files, bytes, entries, err := s.svc.Logs.PurgePreview(cutoff)
	if err != nil {
		s.logger.Error("统计可清理日志失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal_error", "统计可清理日志失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files":   files,
		"bytes":   bytes,
		"entries": entries,
		// 前端据此显示"只能删除 X 之前的记录"
		"cutoff":   cutoff.Format(time.RFC3339),
		"keepDays": int(logMinRetention.Hours() / 24),
	})
}

type logsPurgeReq struct {
	Password string `json:"password"`
}

// handleLogsPurge 删除 2 个月之前的日志文件。
func (s *Server) handleLogsPurge(w http.ResponseWriter, r *http.Request) {
	var req logsPurgeReq
	if !decodeJSON(w, r, &req) {
		return
	}
	// 清除日志是不可逆操作，必须现输一次管理员密码。
	if !s.verifyReauth(w, r, req.Password) {
		return
	}
	cutoff := logPurgeCutoff()
	files, bytes, err := s.svc.Logs.PurgeOlderThan(cutoff)
	if err != nil {
		s.logger.Error("清除日志失败", "err", err)
		s.svc.Audit(s.actor(r), audit.ActionLogsPurge, cutoff.Format(time.RFC3339),
			audit.StatusError, 0, err.Error())
		s.writeCoreErr(w, err)
		return
	}
	// 注意：这条审计本身写在"当前"日志文件里，不会随本次清理被删掉。
	s.svc.Audit(s.actor(r), audit.ActionLogsPurge, cutoff.Format("2006-01-02"),
		audit.StatusOK, bytes, fmt.Sprintf("删除 %d 个日志文件，共 %s", files, state.FormatSize(bytes)))
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "bytes": bytes})
}

// ---------------------------------------------------------------------------
// 用户管理（仅管理端，且每次写操作都要管理员密码）
// ---------------------------------------------------------------------------

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	users := s.svc.State.Users()
	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, s.viewOf(u, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users":           out,
		"permissionNames": state.UserPermissions,
		"defaults": map[string]any{
			"quota":         s.svc.Settings().DefaultQuota,
			"rateLimitMbps": s.svc.Settings().DefaultRateLimit,
		},
	})
}

type createUserReq struct {
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	DisplayName   string   `json:"displayName"`
	Root          string   `json:"root"`
	Quota         *int64   `json:"quota"`
	RateLimitMbps *int     `json:"rateLimitMbps"`
	IsAdmin       bool     `json:"isAdmin"`
	Permissions   []string `json:"permissions"`
	AdminPassword string   `json:"adminPassword"`
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireManagementReauth(w, r, req.AdminPassword) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if err := state.ValidateUsername(req.Username); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	if _, exists := s.svc.State.User(req.Username); exists {
		writeErr(w, http.StatusConflict, "already_exists", "该账号已存在")
		return
	}
	if err := checkPasswordStrength(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "密码加密失败")
		return
	}
	settings := s.svc.Settings()
	nu := &state.User{
		Username: req.Username, DisplayName: strings.TrimSpace(req.DisplayName),
		PasswordHash: hash, IsAdmin: req.IsAdmin,
		Permissions: map[string]bool{}, CreatedAt: time.Now().UTC(),
	}
	if req.Quota != nil {
		nu.Quota = *req.Quota
	} else {
		nu.Quota = int64(settings.DefaultQuota)
	}
	if req.RateLimitMbps != nil {
		nu.RateLimitMbps = *req.RateLimitMbps
	} else {
		nu.RateLimitMbps = settings.DefaultRateLimit
	}
	// 管理员有且只有一个：不提供"再建一个管理员"的能力。
	if req.IsAdmin {
		writeErr(w, http.StatusBadRequest, "admin_unique",
			"系统只有一个管理员账号，不能新建管理员；如需提升权限请调整该账号的权限位")
		return
	}
	{
		root := strings.TrimSpace(req.Root)
		if root == "" {
			root = strings.ToLower(req.Username)
		}
		cleanRoot, err := fsops.CleanRoot(root)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_root", err.Error())
			return
		}
		if clash := s.rootConflict(cleanRoot, req.Username); clash != "" {
			writeErr(w, http.StatusBadRequest, "root_conflict",
				"存储目录 "+cleanRoot+" 与账号 "+clash+" 的目录重复或互相嵌套，请换一个")
			return
		}
		nu.Root = cleanRoot
		perms := req.Permissions
		if len(perms) == 0 {
			perms = append([]string{}, state.UserPermissions...)
		}
		for _, p := range perms {
			if !state.ValidPermission(p) {
				writeErr(w, http.StatusBadRequest, "invalid_permission", "未知权限位 "+p)
				return
			}
			if !state.ValidUserPermission(p) {
				writeErr(w, http.StatusBadRequest, "admin_only_permission",
					"权限 "+p+" 只属于管理员（用户管理），不能分配给普通账号")
				return
			}
			nu.Permissions[p] = true
		}
		// 用户配额不能超过空间总容量，也不能把已分配额度撑爆。
		if err := s.checkQuotaBudget(nu.Username, nu.Quota); err != nil {
			writeErr(w, http.StatusBadRequest, "quota_budget", err.Error())
			return
		}
	}
	if err := s.svc.State.PutUser(nu); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_user", err.Error())
		return
	}
	if nu.Root != "" {
		if _, err := s.svc.FS.EnsureRoot(nu.Root); err != nil {
			// 账号记录这时已经落盘了。不回滚就会留下一个"界面说失败、库里却有"的幽灵账号，
			// 下次同名再建又会撞 already_exists，用户永远查不出原因。
			_ = s.svc.State.DeleteUser(nu.Username)
			s.svc.Audit(s.actor(r), audit.ActionUserCreate, nu.Username, audit.StatusDenied, 0,
				"创建存储目录失败，账号已回滚："+err.Error())
			writeErr(w, http.StatusInternalServerError, "internal_error",
				"创建存储空间失败，账号未创建："+err.Error())
			return
		}
	}
	s.svc.Audit(s.actor(r), audit.ActionUserCreate, nu.Username, audit.StatusOK, 0, "")
	writeJSON(w, http.StatusOK, map[string]any{"user": s.viewOf(nu, false)})
}

type patchUserReq struct {
	DisplayName   *string   `json:"displayName"`
	NewPassword   *string   `json:"newPassword"`
	Root          *string   `json:"root"`
	Quota         *int64    `json:"quota"`
	RateLimitMbps *int      `json:"rateLimitMbps"`
	Disabled      *bool     `json:"disabled"`
	IsAdmin       *bool     `json:"isAdmin"`
	Permissions   *[]string `json:"permissions"`
	AdminPassword string    `json:"adminPassword"`
}

func (s *Server) handleUserPatch(w http.ResponseWriter, r *http.Request) {
	var req patchUserReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireManagementReauth(w, r, req.AdminPassword) {
		return
	}
	name := r.PathValue("username")
	u, ok := s.svc.State.User(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "user_not_found", "账号不存在")
		return
	}
	next := u.Clone()
	if req.DisplayName != nil {
		next.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.NewPassword != nil && *req.NewPassword != "" {
		if err := checkPasswordStrength(*req.NewPassword); err != nil {
			writeErr(w, http.StatusBadRequest, "weak_password", err.Error())
			return
		}
		hash, err := auth.HashPassword(*req.NewPassword)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", "密码加密失败")
			return
		}
		next.PasswordHash = hash
	}
	if req.Quota != nil {
		next.Quota = *req.Quota
	}
	if req.RateLimitMbps != nil {
		next.RateLimitMbps = *req.RateLimitMbps
	}
	if req.Root != nil {
		cleanRoot, err := fsops.CleanRoot(*req.Root)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_root", err.Error())
			return
		}
		next.Root = cleanRoot
	}
	if next.Root != "" {
		if clash := s.rootConflict(next.Root, u.Username); clash != "" {
			writeErr(w, http.StatusBadRequest, "root_conflict",
				"存储目录 "+next.Root+" 与账号 "+clash+" 的目录重复或互相嵌套，请换一个")
			return
		}
	}
	// 管理员有且只有一个：不允许把普通账号提升为管理员，也不允许把管理员降级
	// （降级会让系统失去唯一管理员，等价于把自己锁在门外）。
	if req.IsAdmin != nil && *req.IsAdmin != u.IsAdmin {
		if *req.IsAdmin {
			writeErr(w, http.StatusBadRequest, "admin_unique",
				"系统只有一个管理员账号，不能把其他账号提升为管理员")
		} else {
			writeErr(w, http.StatusBadRequest, "last_admin",
				"系统必须保留管理员账号，不能取消管理员身份")
		}
		return
	}
	if req.Permissions != nil {
		perms := map[string]bool{}
		for _, p := range *req.Permissions {
			if !state.ValidPermission(p) {
				writeErr(w, http.StatusBadRequest, "invalid_permission", "未知权限位 "+p)
				return
			}
			if !state.ValidUserPermission(p) {
				writeErr(w, http.StatusBadRequest, "admin_only_permission",
					"权限 "+p+" 只属于管理员（用户管理），不能分配给普通账号")
				return
			}
			perms[p] = true
		}
		next.Permissions = perms
	}
	if req.Disabled != nil {
		next.Disabled = *req.Disabled
	}
	// 普通账号的配额也要受空间总容量约束
	if !next.IsAdmin {
		if err := s.checkQuotaBudget(next.Username, next.Quota); err != nil {
			writeErr(w, http.StatusBadRequest, "quota_budget", err.Error())
			return
		}
	}
	// 保护：不能让系统失去最后一个可用的管理员，也不能把自己锁在门外
	if u.IsAdmin && (!next.IsAdmin || next.Disabled) && u.Username == userFrom(r).Username {
		writeErr(w, http.StatusBadRequest, "self_lockout", "不能取消或禁用当前登录的管理员账号")
		return
	}
	if u.IsAdmin && (!next.IsAdmin || next.Disabled) && s.svc.State.AdminCount() <= 1 {
		writeErr(w, http.StatusBadRequest, "last_admin", "系统必须保留至少一个可用的管理员")
		return
	}
	if err := s.svc.State.PutUser(next); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_user", err.Error())
		return
	}
	if next.Root != "" {
		_, _ = s.svc.FS.EnsureRoot(next.Root)
	}
	s.svc.Audit(s.actor(r), audit.ActionUserUpdate, next.Username, audit.StatusOK, 0, "")
	writeJSON(w, http.StatusOK, map[string]any{"user": s.viewOf(next, true)})
}

// checkQuotaBudget 保证"所有普通账号的配额之和 ≤ 空间总容量"。
// 例：空间 10 GiB，A 已分配 8 GiB，则 B 最多只能再分 2 GiB。
func (s *Server) checkQuotaBudget(self string, quota int64) error {
	st := s.svc.Settings()
	if st.SpaceQuota == 0 {
		return nil // 空间不限，配额随便分
	}
	total := int64(st.SpaceQuota)
	if quota < 0 {
		return errors.New("配额不能为负数")
	}
	if quota == 0 {
		return fmt.Errorf("空间总容量为 %s，必须给账号分配一个明确配额（不能选不限）", state.FormatSize(total))
	}
	if quota > total {
		return fmt.Errorf("配额 %s 超过空间总容量 %s", state.FormatSize(quota), state.FormatSize(total))
	}
	var others int64
	for _, u := range s.svc.State.Users() {
		if u == nil || u.IsAdmin || u.Username == self {
			continue
		}
		others += u.Quota
	}
	if others+quota > total {
		return fmt.Errorf("已分配给其他账号 %s，再分 %s 会超过空间总容量 %s（最多还能分 %s）",
			state.FormatSize(others), state.FormatSize(quota), state.FormatSize(total),
			state.FormatSize(total-others))
	}
	return nil
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireManagementReauth(w, r, "") {
		return
	}
	name := r.PathValue("username")
	u, ok := s.svc.State.User(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "user_not_found", "账号不存在")
		return
	}
	if u.Username == userFrom(r).Username {
		writeErr(w, http.StatusBadRequest, "self_delete", "不能删除当前登录的账号")
		return
	}
	if u.IsAdmin && s.svc.State.AdminCount() <= 1 {
		writeErr(w, http.StatusBadRequest, "last_admin", "系统必须保留至少一个可用的管理员")
		return
	}
	deleteFiles := r.URL.Query().Get("deleteFiles") == "1"
	if deleteFiles && u.Root != "" {
		if rootAbs, err := s.svc.FS.RootPath(u.Root); err == nil {
			if err := os.RemoveAll(rootAbs); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", "删除文件失败："+err.Error())
				return
			}
		}
		for _, e := range s.svc.State.TrashEntries() {
			if e.Root == u.Root {
				_ = os.RemoveAll(filepath.Join(s.cfg.TrashPath(), e.ID))
				_ = s.svc.State.DeleteTrashEntry(e.ID)
			}
		}
	}
	for _, sess := range s.svc.State.UploadSessions() {
		if sess.Username == u.Username {
			_ = os.RemoveAll(filepath.Join(s.cfg.UploadPath(), sess.ID))
			_ = s.svc.State.DeleteUploadSession(sess.ID)
		}
	}
	if err := s.svc.State.DeleteUser(u.Username); err != nil {
		s.writeCoreErr(w, err)
		return
	}
	s.svc.Audit(s.actor(r), audit.ActionUserDelete, u.Username, audit.StatusOK, 0,
		map[bool]string{true: "同时删除了其文件", false: "保留了其文件"}[deleteFiles])
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 运行期设置
// ---------------------------------------------------------------------------

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings := s.svc.Settings()
	restart := []string{}
	if settings.SpaceDir != "" && settings.SpaceDir != s.cfg.SpaceDir {
		restart = append(restart, "spaceDir")
	}
	if settings.Listen != "" && settings.Listen != s.cfg.Listen {
		restart = append(restart, "listen")
	}
	if (settings.TLSCert != "" || settings.TLSKey != "") &&
		(settings.TLSCert != s.cfg.TLSCert || settings.TLSKey != s.cfg.TLSKey) {
		restart = append(restart, "tls")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":        settings,
		"restartRequired": restart,
		"effective": map[string]any{
			"configFile":      s.cfg.Path,
			"listen":          s.cfg.Listen,
			"spaceDir":        s.cfg.SpaceDir,
			"defaultSpaceDir": config.DefaultSpaceDir(),
			"tlsEnabled":      s.cfg.TLSEnabled(),
			"tlsCert":         s.cfg.TLSCert,
			"tlsKey":          s.cfg.TLSKey,
			"officeAvailable": s.svc.OfficeAvailable(),
			"version":         Version,
			"permissionNames": state.UserPermissions,
		},
	})
}

type settingsPatch struct {
	SpaceDir          *string          `json:"spaceDir"`
	Listen            *string          `json:"listen"`
	TLSCert           *string          `json:"tlsCert"`
	TLSKey            *string          `json:"tlsKey"`
	SpaceQuota        *state.SizeValue `json:"spaceQuota"`
	DefaultQuota      *state.SizeValue `json:"defaultQuota"`
	DefaultRateLimit  *int             `json:"defaultRateLimitMbps"`
	TrashRetention    *string          `json:"trashRetention"`
	LogRetention      *string          `json:"logRetention"`
	UploadSessionTTL  *string          `json:"uploadSessionTTL"`
	MaxUploadSize     *state.SizeValue `json:"maxUploadSize"`
	ChunkSize         *state.SizeValue `json:"chunkSize"`
	SessionTTL        *string          `json:"sessionTTL"`
	TrustProxy        *bool            `json:"trustProxy"`
	AccessLogDisabled *bool            `json:"accessLogDisabled"`
	OfficePreview     *string          `json:"officePreview"`
	AdminPassword     string           `json:"adminPassword"`
}

func (s *Server) handleSettingsPatch(w http.ResponseWriter, r *http.Request) {
	var req settingsPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireManagementReauth(w, r, req.AdminPassword) {
		return
	}
	next := s.svc.Settings()
	restart := []string{}

	if req.SpaceQuota != nil {
		next.SpaceQuota = *req.SpaceQuota
	}
	if req.DefaultQuota != nil {
		next.DefaultQuota = *req.DefaultQuota
	}
	if req.DefaultRateLimit != nil {
		next.DefaultRateLimit = *req.DefaultRateLimit
	}
	if req.MaxUploadSize != nil {
		next.MaxUploadSize = *req.MaxUploadSize
	}
	if req.ChunkSize != nil {
		next.ChunkSize = *req.ChunkSize
	}
	if req.TrashRetention != nil {
		next.TrashRetention = strings.TrimSpace(*req.TrashRetention)
	}
	if req.LogRetention != nil {
		next.LogRetention = strings.TrimSpace(*req.LogRetention)
	}
	if req.UploadSessionTTL != nil {
		next.UploadSessionTTL = strings.TrimSpace(*req.UploadSessionTTL)
	}
	if req.SessionTTL != nil {
		next.SessionTTL = strings.TrimSpace(*req.SessionTTL)
	}
	if req.TrustProxy != nil {
		next.TrustProxy = *req.TrustProxy
	}
	if req.AccessLogDisabled != nil {
		next.AccessLogDisabled = *req.AccessLogDisabled
	}
	if req.OfficePreview != nil {
		next.OfficePreview = strings.TrimSpace(*req.OfficePreview)
	}
	if req.SpaceDir != nil {
		want := strings.TrimSpace(*req.SpaceDir)
		if want != "" && want != s.cfg.SpaceDir {
			if !filepath.IsAbs(want) {
				writeErr(w, http.StatusBadRequest, "invalid_setting", "存储位置必须是绝对路径")
				return
			}
			if err := probeWritable(want); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_setting", "该位置不可写："+err.Error())
				return
			}
			// 目标位置没有元数据时，直接在管理端完成迁移（把当前空间整棵复制过去），
			// 不再要求用户去命令行跑 migrate-space。
			if !fileExists(filepath.Join(want, "state.json")) && !spaceIsEmpty(s.cfg.SpaceDir) {
				if isNestedPath(want, s.cfg.SpaceDir) || isNestedPath(s.cfg.SpaceDir, want) {
					writeErr(w, http.StatusBadRequest, "invalid_setting",
						"新存储位置不能是当前空间的父目录或子目录")
					return
				}
				copied, err := copyTree(s.cfg.SpaceDir, want)
				if err != nil {
					writeErr(w, http.StatusInternalServerError, "internal_error",
						"迁移数据失败："+err.Error())
					return
				}
				if !fileExists(filepath.Join(want, "state.json")) {
					writeErr(w, http.StatusInternalServerError, "internal_error",
						"迁移后目标位置仍缺少 state.json，已中止")
					return
				}
				s.svc.Audit(s.actor(r), audit.ActionSettingsSet, "spaceDir", audit.StatusOK, copied,
					"已把空间目录迁移到 "+want)
			}
			if err := config.SetValue(s.cfg.Path, "space_dir", want); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", "写入配置文件失败："+err.Error())
				return
			}
			next.SpaceDir = want
			restart = append(restart, "spaceDir")
		}
	}
	// 监听地址：写回 config.ini，重启后生效。
	// Docker 场景下容器内端口必须与 compose 的映射右侧一致，界面会额外提示，
	// 所以这里只负责"把用户的选择正确落盘 + 校验合法性"。
	if req.Listen != nil {
		want := strings.TrimSpace(*req.Listen)
		if err := config.ValidateListen(want); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_setting", err.Error())
			return
		}
		if want == s.cfg.Listen {
			next.Listen = "" // 改回当前值 = 撤销待生效项
		} else {
			if err := config.SetValue(s.cfg.Path, "listen", want); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error",
					"写入配置文件失败："+err.Error())
				return
			}
			next.Listen = want
			restart = append(restart, "listen")
		}
	}

	// TLS：证书与私钥必须同时存在或同时留空；非空时先确认文件真的可读，
	// 否则重启后进程直接起不来，用户面对的是"改了设置，服务不见了"。
	if req.TLSCert != nil || req.TLSKey != nil {
		cert, key := s.cfg.TLSCert, s.cfg.TLSKey
		if next.TLSCert != "" || next.TLSKey != "" {
			cert, key = next.TLSCert, next.TLSKey
		}
		if req.TLSCert != nil {
			cert = strings.TrimSpace(*req.TLSCert)
		}
		if req.TLSKey != nil {
			key = strings.TrimSpace(*req.TLSKey)
		}
		if (cert == "") != (key == "") {
			writeErr(w, http.StatusBadRequest, "invalid_setting", "TLS 证书与私钥必须同时填写或同时留空")
			return
		}
		for _, f := range []string{cert, key} {
			if f == "" {
				continue
			}
			if !filepath.IsAbs(f) {
				writeErr(w, http.StatusBadRequest, "invalid_setting", "证书路径必须是绝对路径："+f)
				return
			}
			if _, statErr := os.Stat(f); statErr != nil {
				writeErr(w, http.StatusBadRequest, "invalid_setting", "读取不到证书文件："+f)
				return
			}
		}
		if cert == s.cfg.TLSCert && key == s.cfg.TLSKey {
			next.TLSCert, next.TLSKey = "", ""
		} else {
			if err := config.SetValue(s.cfg.Path, "tls_cert", cert); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error",
					"写入配置文件失败："+err.Error())
				return
			}
			if err := config.SetValue(s.cfg.Path, "tls_key", key); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error",
					"写入配置文件失败："+err.Error())
				return
			}
			next.TLSCert, next.TLSKey = cert, key
			restart = append(restart, "tls")
		}
	}

	if err := next.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_setting", err.Error())
		return
	}
	// 空间总容量不能小于"已分配给用户的配额之和"，否则那些账号永远无法用满自己的配额。
	if next.SpaceQuota > 0 {
		var allocated int64
		for _, u := range s.svc.State.Users() {
			if u == nil || u.IsAdmin {
				continue
			}
			allocated += u.Quota
		}
		if allocated > int64(next.SpaceQuota) {
			writeErr(w, http.StatusBadRequest, "invalid_setting",
				"空间总容量 "+state.FormatSize(int64(next.SpaceQuota))+
					" 小于已分配给用户的配额之和 "+state.FormatSize(allocated)+"，请先调低用户配额")
			return
		}
	}
	saved, err := s.svc.State.PutSettings(next)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	s.rl.Reset()
	s.svc.Audit(s.actor(r), audit.ActionSettingsSet, "settings", audit.StatusOK, 0,
		"运行期设置已更新")
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": saved, "restartRequired": restart,
	})
}

func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".homevault-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// isNestedPath 判断 child 是否等于 parent 或位于 parent 之下。
func isNestedPath(parent, child string) bool {
	p := strings.TrimRight(filepath.Clean(parent), string(os.PathSeparator))
	c := strings.TrimRight(filepath.Clean(child), string(os.PathSeparator))
	if strings.EqualFold(p, c) {
		return true
	}
	rel, err := filepath.Rel(p, c)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// copyTree 把 src 整棵目录复制到 dst，返回复制的字节数。
// 用于"在管理端直接切换存储位置"：先把数据搬过去，再改 config.ini 并提示重启。
func copyTree(src, dst string) (int64, error) {
	var total int64
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o750)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !d.Type().IsRegular() {
			return nil // 跳过符号链接等特殊文件
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(out, in)
		syncErr := out.Sync()
		closeErr := out.Close()
		total += n
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	})
	return total, err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// spaceIsEmpty 判断当前空间里除了目录骨架之外是否没有任何数据。
func spaceIsEmpty(spaceDir string) bool {
	if fileExists(filepath.Join(spaceDir, "state.json")) {
		return false
	}
	return true
}
