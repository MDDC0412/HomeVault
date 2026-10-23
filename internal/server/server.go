// Package server 是 HTTP 层：路由、中间件、参数解析与错误映射。
// 业务规则一律委托给 core.Service，这一层只做"翻译"与鉴权。
//
// 关于管理员代操作：管理员可以通过 ?user=<账号> 操作他人文件，
// 但**每一个写操作**都必须重新输入管理员密码（不做时间窗缓存），
// 并且每次都会留下审计记录（actingAs 字段标明被操作的账号）。
package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homevault/internal/audit"
	"homevault/internal/auth"
	"homevault/internal/config"
	"homevault/internal/core"
	"homevault/internal/fsops"
	"homevault/internal/state"
)

// Version 由 main 注入。
var Version = "0.1.1"

const sessionCookie = "homevault_session"

// Server 持有 HTTP 层的全部依赖。
type Server struct {
	cfg     *config.Config
	svc     *core.Service
	limiter *auth.Limiter
	rl      *RateLimiter
	logger  *slog.Logger
	static  fs.FS
	started time.Time
}

// New 构造 Server。
func New(cfg *config.Config, svc *core.Service, static fs.FS, logger *slog.Logger) *Server {
	return &Server{
		cfg:     cfg,
		svc:     svc,
		limiter: auth.NewLimiter(),
		rl:      NewRateLimiter(),
		logger:  logger,
		static:  static,
		started: time.Now(),
	}
}

type routeSpec struct {
	method string
	path   string
	perm   string
	// management 为真表示这是管理类写操作，管理员每次都需要重新输入密码。
	management bool
	handler    http.HandlerFunc
}

// Handler 返回完整的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 公开
	mux.HandleFunc("GET /api/healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	routes := []routeSpec{
		{"GET", "/api/auth/me", "", false, s.handleMe},
		{"POST", "/api/auth/logout", "", false, s.handleLogout},
		{"POST", "/api/auth/change-credentials", "", false, s.handleChangeCredentials},

		{"GET", "/api/system", "", false, s.handleSystem},
		{"GET", "/api/system/disks", state.PermUsersManage, false, s.handleDisks},
		{"GET", "/api/stats", state.PermUsersManage, false, s.handleStats},

		{"GET", "/api/search", state.PermRead, false, s.handleSearch},
		{"GET", "/api/preview", state.PermRead, false, s.handlePreviewPlan},
		{"GET", "/api/preview/office", state.PermRead, false, s.handleOfficePreview},

		{"GET", "/api/fs/list", state.PermRead, false, s.handleList},
		{"GET", "/api/fs/download", state.PermRead, false, s.handleDownload},
		{"POST", "/api/fs/mkdir", state.PermModify, false, s.handleMkdir},
		{"POST", "/api/fs/rename", state.PermModify, false, s.handleRename},
		{"POST", "/api/fs/move", state.PermModify, false, s.handleMove},
		{"POST", "/api/fs/copy", state.PermModify, false, s.handleCopy},
		{"POST", "/api/fs/delete", state.PermDelete, false, s.handleDelete},
		{"POST", "/api/fs/upload", state.PermUpload, false, s.handleSimpleUpload},

		{"GET", "/api/fs/text", state.PermRead, false, s.handleTextRead},
		{"POST", "/api/fs/text", state.PermModify, false, s.handleTextSave},

		{"POST", "/api/upload/init", state.PermUpload, false, s.handleUploadInit},
		{"GET", "/api/upload", state.PermUpload, false, s.handleUploadList},
		{"GET", "/api/upload/{id}", state.PermUpload, false, s.handleUploadStatus},
		{"PUT", "/api/upload/{id}/{index}", state.PermUpload, false, s.handleUploadChunk},
		{"POST", "/api/upload/{id}/complete", state.PermUpload, false, s.handleUploadComplete},
		{"DELETE", "/api/upload/{id}", state.PermUpload, false, s.handleUploadCancel},

		{"GET", "/api/trash", state.PermTrashView, false, s.handleTrashList},
		{"POST", "/api/trash/restore", state.PermTrashManage, false, s.handleTrashRestore},
		{"POST", "/api/trash/delete", state.PermTrashManage, false, s.handleTrashPurge},
		{"POST", "/api/trash/empty", state.PermTrashManage, false, s.handleTrashEmpty},

		{"GET", "/api/logs", state.PermLogsView, false, s.handleLogs},
		// 日志清除属于管理端专属：路由侧只要求登录，实际用 adminOnlyPath 拦住非管理员，
		// 并额外要求现场输入管理员密码。
		{"GET", "/api/logs/purge", state.PermLogsView, false, s.handleLogsPurgeInfo},
		{"POST", "/api/logs/purge", state.PermLogsView, false, s.handleLogsPurge},

		{"GET", "/api/users", state.PermUsersManage, false, s.handleUsersList},
		{"POST", "/api/users", state.PermUsersManage, true, s.handleUserCreate},
		{"PATCH", "/api/users/{username}", state.PermUsersManage, true, s.handleUserPatch},
		{"DELETE", "/api/users/{username}", state.PermUsersManage, true, s.handleUserDelete},

		{"GET", "/api/settings", state.PermUsersManage, false, s.handleSettingsGet},
		{"PATCH", "/api/settings", state.PermUsersManage, true, s.handleSettingsPatch},
	}
	for _, r := range routes {
		mux.HandleFunc(r.method+" "+r.path, s.wrap(r, r.handler))
	}

	mux.HandleFunc("GET /", s.handleStatic)
	return s.recoverer(s.accessLog(s.securityHeaders(mux)))
}

// ---------------------------------------------------------------------------
// 中间件
// ---------------------------------------------------------------------------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("请求处理 panic", "err", rec, "path", r.URL.Path)
				writeErr(w, http.StatusInternalServerError, "internal_error", "服务端内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.svc.Settings().AccessLogDisabled {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.logger.Info("http",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"bytes", rec.written, "duration", time.Since(start).Round(time.Millisecond).String(),
			"ip", s.clientIP(r))
	})
}

// 预览类接口需要用 <iframe> 内嵌到页面里：
//
//	/api/preview/office   Office 转成的 PDF
//	/api/fs/download      PDF / 图片 / 音视频的 inline 展示
//
// 因此这些路径必须放行同源嵌入；其余页面保持 DENY / frame-ancestors 'none'，
// 避免整个站点被第三方站点套框（点击劫持）。
func isFrameablePreviewPath(p string) bool {
	switch p {
	case "/api/preview", "/api/preview/office", "/api/fs/download":
		return true
	}
	return false
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		if isFrameablePreviewPath(r.URL.Path) {
			// SAMEORIGIN 而不是 DENY：预览要能被本站的 iframe 加载。
			// CSP 的 frame-ancestors 用 'self' 与之对齐（两者都是"只允许同源"）。
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; "+
					"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; "+
					"object-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'self'")
		} else {
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; "+
					"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; "+
					"object-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		}
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Flush 透传，保证流式响应（分片上传/预览下载）不被缓冲破坏。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// lockedAllowed 列出"必须改凭据"状态下仍可调用的接口。
func lockedAllowed(path string) bool {
	switch path {
	case "/api/auth/me", "/api/auth/logout", "/api/auth/change-credentials", "/api/healthz":
		return true
	}
	return false
}

// wrap 是鉴权包装：解析会话 → 解析代操作目标 → 校验权限。
func (s *Server) wrap(spec routeSpec, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := s.authenticate(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录或会话已过期")
			return
		}
		if actor.Disabled {
			writeErr(w, http.StatusForbidden, "disabled", "账号已被禁用")
			return
		}
		if actor.MustChangeCredentials && !lockedAllowed(r.URL.Path) {
			writeErr(w, http.StatusForbidden, "must_change_credentials",
				"请先修改默认账号或密码，之后才能进行其他操作")
			return
		}
		// 代操作目标（仅管理员可用）。普通用户即使写的是自己的账号名也一律拒绝，
		// 避免"带 ?user= 就绕过权限位"这类边界。
		target := actor
		asWho := ""
		if want := strings.TrimSpace(r.URL.Query().Get("user")); want != "" {
			if !actor.IsAdmin {
				writeErr(w, http.StatusForbidden, "forbidden", "只有管理员可以操作用户文件")
				return
			}
			if want != actor.Username {
				tu, ok := s.svc.State.User(want)
				if !ok {
					writeErr(w, http.StatusNotFound, "user_not_found", "目标用户不存在")
					return
				}
				target, asWho = tu, want
				s.svc.Audit(core.Actor{Username: actor.Username, ActingAs: asWho, IP: s.clientIP(r),
					UserAgent: r.Header.Get("User-Agent")}, audit.ActionAdminAccess, want, audit.StatusOK, 0,
					"管理员访问用户文件")
			}
		}
		if spec.perm != "" && !actor.Can(spec.perm) && asWho == "" {
			act := core.Actor{Username: actor.Username, IP: s.clientIP(r), UserAgent: r.Header.Get("User-Agent")}
			s.svc.Audit(act, audit.ActionAdminAccess, r.URL.Path, audit.StatusDenied, 0, "缺少权限 "+spec.perm)
			writeErr(w, http.StatusForbidden, "forbidden", "没有执行该操作的权限")
			return
		}
		// 账号管理与服务端设置永远只允许管理员操作：
		// 否则拥有 users_manage 的普通用户可以自己造一个管理员账号（提权）。
		if !actor.IsAdmin && adminOnlyPath(r.URL.Path) {
			act := core.Actor{Username: actor.Username, IP: s.clientIP(r), UserAgent: r.Header.Get("User-Agent")}
			s.svc.Audit(act, audit.ActionAdminAccess, r.URL.Path, audit.StatusDenied, 0, "非管理员访问管理接口")
			writeErr(w, http.StatusForbidden, "forbidden", "只有管理员可以操作账号与服务端设置")
			return
		}
		ctx := withTarget(withUser(r, actor), target, asWho)
		h(w, r.WithContext(ctx))
	}
}

// authenticate 从 Cookie 或 Authorization 头解析会话。
func (s *Server) authenticate(r *http.Request) (*state.User, bool) {
	token := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		token = c.Value
	}
	if token == "" {
		if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			token = strings.TrimSpace(after)
		}
	}
	if token == "" {
		return nil, false
	}
	username, version, err := s.svc.Tokens.VerifySession(token)
	if err != nil {
		return nil, false
	}
	user, ok := s.svc.State.User(username)
	if !ok {
		return nil, false
	}
	// 令牌里带着签发时的密码版本：改过密码的旧令牌必须立刻失效。
	if version != auth.PasswordVersion(user.PasswordHash) {
		return nil, false
	}
	return user, true
}

func (s *Server) setSession(w http.ResponseWriter, username, passwordHash string) {
	ttl := s.svc.Settings().SessionDur()
	token, exp := s.svc.Tokens.IssueWithTTL(username, auth.PasswordVersion(passwordHash), ttl)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.cfg.TLSEnabled(), Expires: exp,
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.cfg.TLSEnabled(), MaxAge: -1,
	})
}

// clientIP 返回客户端 IP；仅在信任反向代理时才采信转发头。
func (s *Server) clientIP(r *http.Request) string {
	if s.svc.Settings().TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
				return ip
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// actor 构造审计主体（含代操作信息）。
func (s *Server) actor(r *http.Request) core.Actor {
	a := core.Actor{IP: s.clientIP(r), UserAgent: r.Header.Get("User-Agent")}
	if u := userFrom(r); u != nil {
		a.Username = u.Username
	}
	a.ActingAs = actingAs(r)
	return a
}

// ---------------------------------------------------------------------------
// 二次密码（管理员写操作）
// ---------------------------------------------------------------------------

// reauthSecret 从请求体字段或请求头取管理员密码。
func reauthSecret(r *http.Request, bodyPw string) string {
	if bodyPw != "" {
		return bodyPw
	}
	if v := r.Header.Get("X-LD-Password-B64"); v != "" {
		if b, err := base64.StdEncoding.DecodeString(v); err == nil {
			return string(b)
		}
	}
	return r.Header.Get("X-LD-Password")
}

// verifyReauth 校验管理员密码；失败时已写好响应。
func (s *Server) verifyReauth(w http.ResponseWriter, r *http.Request, bodyPw string) bool {
	actor := userFrom(r)
	if actor == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录")
		return false
	}
	secret := reauthSecret(r, bodyPw)
	if secret == "" {
		writeErr(w, http.StatusPreconditionRequired, "reauth_required", "该操作需要再次输入管理员密码")
		return false
	}
	ip := s.clientIP(r)
	if !s.limiter.Allowed(actor.Username, ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", "密码错误次数过多，请稍后再试")
		return false
	}
	if !auth.VerifyPassword(actor.PasswordHash, secret) {
		s.limiter.Fail(actor.Username, ip)
		s.svc.Audit(s.actor(r), audit.ActionReauth, r.URL.Path, audit.StatusDenied, 0, "二次密码校验失败")
		writeErr(w, http.StatusForbidden, "reauth_failed", "管理员密码不正确")
		return false
	}
	s.limiter.Reset(actor.Username, ip)
	return true
}

// requireReauth 判断本次请求是否需要二次密码：
// 管理员操作他人文件时的**任何写操作**都需要。
func (s *Server) requireReauth(w http.ResponseWriter, r *http.Request, bodyPw string) bool {
	actor, target := userFrom(r), targetFrom(r)
	if actor == nil || target == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录")
		return false
	}
	if !actor.IsAdmin || target.Username == actor.Username {
		return true // 操作自己的文件不需要额外密码
	}
	return s.verifyReauth(w, r, bodyPw)
}

// requireManagementReauth 用于用户管理与设置修改：管理员每次都要输密码。
func (s *Server) requireManagementReauth(w http.ResponseWriter, r *http.Request, bodyPw string) bool {
	actor := userFrom(r)
	if actor == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录")
		return false
	}
	if !actor.IsAdmin {
		return true
	}
	return s.verifyReauth(w, r, bodyPw)
}

// permissionLabelsFor 返回"这个账号可以被分配的权限位"：
// 管理员看到全部，普通账号只看到 read/upload/modify/delete（回收站与日志属于管理员职能）。
func permissionLabelsFor(u *state.User) []string {
	if u != nil && u.IsAdmin {
		return state.AllPermissions
	}
	return state.UserPermissions
}

// adminOnlyPath 判断某个接口是否只允许管理员访问。
func adminOnlyPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "/api/users"), strings.HasPrefix(p, "/api/settings"):
		return true
	case p == "/api/system/disks":
		return true
	case strings.HasPrefix(p, "/api/logs/purge"):
		// 清除日志只有管理员能做（普通账号即使有 logs_view 也不行）。
		return true
	}
	return false
}

// rootConflict 检查某个存储根是否与**其他**账号的根冲突，返回冲突账号名。
// 规则：不允许两个账号使用相同根（Windows 上大小写不敏感），
// 也不允许父子嵌套（否则一个账号能看见另一个账号的文件、用量还会互相计入）。
func (s *Server) rootConflict(root, self string) string {
	key := strings.ToLower(strings.ReplaceAll(root, "\\", "/"))
	key = strings.Trim(key, "/")
	if key == "" {
		return ""
	}
	for _, other := range s.svc.State.Users() {
		if other.Username == self || other.Root == "" {
			continue
		}
		otherKey := strings.Trim(strings.ToLower(strings.ReplaceAll(other.Root, "\\", "/")), "/")
		switch {
		case otherKey == key:
			return other.Username
		case strings.HasPrefix(key, otherKey+"/"), strings.HasPrefix(otherKey, key+"/"):
			return other.Username
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 响应辅助
// ---------------------------------------------------------------------------

type errBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	// 429 必须带 Retry-After，客户端（含浏览器与脚本）才能正确退避。
	if status == http.StatusTooManyRequests {
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", "60")
		}
	}
	writeJSON(w, status, errBody{Error: msg, Code: code})
}

// writeCoreErr 把业务错误映射为 HTTP 状态码。
func (s *Server) writeCoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, core.ErrExists):
		writeErr(w, http.StatusConflict, "already_exists", err.Error())
	case errors.Is(err, core.ErrForbidden):
		writeErr(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, core.ErrNoStorage), errors.Is(err, fsops.ErrNoRoot):
		// 管理员没有自己的存储空间：必须先在管理页面选择目标用户。
		writeErr(w, http.StatusForbidden, "admin_no_storage",
			"管理员没有自己的存储空间，请在管理页面选择目标用户后再操作")
	case errors.Is(err, core.ErrQuota):
		writeErr(w, http.StatusInsufficientStorage, "quota_exceeded", err.Error())
	case errors.Is(err, core.ErrSpace):
		writeErr(w, http.StatusInsufficientStorage, "space_full", err.Error())
	case errors.Is(err, core.ErrTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, core.ErrPreviewUnavailable):
		writeErr(w, http.StatusConflict, "office_preview_unavailable", err.Error())
	case errors.Is(err, core.ErrConflict):
		writeErr(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, core.ErrNotText):
		writeErr(w, http.StatusUnsupportedMediaType, "not_text", err.Error())
	case errors.Is(err, core.ErrTextTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, "text_too_large", err.Error())
	case errors.Is(err, core.ErrBadRequest), errors.Is(err, core.ErrUploadState):
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.logger.Error("业务处理失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal_error", "服务端内部错误："+err.Error())
	}
}

// decodeJSON 解析请求体并拒绝未知字段（配置项写错名字要立刻报错）。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeJSONLimit(w, r, dst, 1<<20)
}

// decodeJSONLimit 同 decodeJSON，但可指定请求体上限。
// 文本保存可能达到数 MiB，不能套用默认的 1 MiB 限制。
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "请求体不是合法 JSON："+err.Error())
		return false
	}
	return true
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryInt64(r *http.Request, key string, def int64) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
