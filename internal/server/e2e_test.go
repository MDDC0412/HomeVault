package server_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homevault/internal/audit"
	"homevault/internal/auth"
	"homevault/internal/config"
	"homevault/internal/core"
	"homevault/internal/fsops"
	"homevault/internal/server"
	"homevault/internal/state"
)

// 这是一个端到端测试：真实启动 HTTP 处理器，用真实 Cookie 走完
// 登录 → 建管理员/用户 → 建目录 → 上传（简单 + 分片）→ 列表 → 下载 →
// 搜索 → 删除 → 恢复 → 回收站 → 审计日志 → 设置 → 配额 → 强制改凭据 的完整链路。

type env struct {
	t       *testing.T
	ts      *httptest.Server
	svc     *core.Service
	store   *state.Store
	cfg     *config.Config
	client  *http.Client
	admin   string
	baseURL string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.ini")
	if err := os.WriteFile(cfgPath, []byte("listen = \"127.0.0.1:0\"\nspace_dir = \""+
		filepath.ToSlash(filepath.Join(root, "HomeVaultSpace"))+"\"\n"), 0o600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if err := cfg.EnsureLayout(); err != nil {
		t.Fatalf("创建空间失败：%v", err)
	}
	store, err := state.Open(cfg.StatePath())
	if err != nil {
		t.Fatalf("打开元数据失败：%v", err)
	}
	// 测试实例把空间总容量放大，避免"用户配额之和 ≤ 空间容量"把无关用例卡住；
	// 配额预算规则本身由 TestUserQuotaBudget 专门覆盖。
	settings := state.DefaultSettings()
	settings.SpaceQuota = 1 << 40
	settings.DefaultQuota = 10 << 30
	if _, err := store.EnsureSettings(settings); err != nil {
		t.Fatalf("初始化设置失败：%v", err)
	}
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatal(err)
	}
	perms := map[string]bool{}
	for _, p := range state.UserPermissions {
		perms[p] = true
	}
	if err := store.PutUser(&state.User{
		Username: "admin", PasswordHash: hash, IsAdmin: true, Root: "",
		Permissions: perms, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("创建管理员失败：%v", err)
	}
	ops, err := fsops.New(cfg.FilesPath())
	if err != nil {
		t.Fatal(err)
	}
	logs, err := audit.New(cfg.AuditPath())
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokenManager(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := core.New(cfg, store, ops, logs, tokens)
	static := fstest.MapFS{
		"index.html": {Data: []byte("<html>user</html>")},
		"admin.html": {Data: []byte("<html>admin</html>")},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, svc, static, logger)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &env{
		t: t, ts: ts, svc: svc, store: store, cfg: cfg,
		client: &http.Client{Jar: jar, Timeout: 30 * time.Second},
		admin:  "admin", baseURL: ts.URL,
	}
}

type resp struct {
	status int
	body   []byte
	header http.Header
}

func (e *env) do(method, path string, body any, headers map[string]string) resp {
	e.t.Helper()
	var reader io.Reader
	switch v := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(v)
	case io.Reader:
		reader = v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			e.t.Fatalf("序列化请求失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.baseURL+path, reader)
	if err != nil {
		e.t.Fatalf("构造请求失败：%v", err)
	}
	if body != nil && reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s 请求失败：%v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: raw}
}

func (e *env) json(method, path string, body any) resp {
	return e.do(method, path, body, nil)
}

func (e *env) mustJSON(method, path string, body any, want int) map[string]any {
	e.t.Helper()
	r := e.json(method, path, body)
	if r.status != want {
		e.t.Fatalf("%s %s 期望 %d，实际 %d：%s", method, path, want, r.status, string(r.body))
	}
	if len(r.body) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		e.t.Fatalf("%s %s 响应不是 JSON：%s", method, path, string(r.body))
	}
	return out
}

func (e *env) loginAs(user, password string) {
	e.t.Helper()
	res := e.mustJSON("POST", "/api/auth/login",
		map[string]string{"username": user, "password": password}, http.StatusOK)
	u, _ := res["user"].(map[string]any)
	if u == nil {
		e.t.Fatalf("登录响应缺少 user 字段：%v", res)
	}
}

func (e *env) logout() {
	e.json("POST", "/api/auth/logout", map[string]any{})
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)

	// 1. 健康检查
	e.mustJSON("GET", "/api/healthz", nil, http.StatusOK)

	// 2. 未登录访问被拒绝
	r := e.json("GET", "/api/fs/list", nil)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d", r.status)
	}

	// 3. 管理员登录
	e.loginAs("admin", "admin")
	me := e.mustJSON("GET", "/api/auth/me", nil, http.StatusOK)
	user := me["user"].(map[string]any)
	if user["isAdmin"] != true {
		t.Fatalf("admin 应当是管理员：%v", user)
	}
	if home, _ := me["home"].(string); home != "/admin/" {
		t.Fatalf("管理员 home 应为 /admin/，实际 %q", home)
	}

	// 4. 管理员没有自己的存储空间
	r = e.json("GET", "/api/fs/list", nil)
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "admin_no_storage") {
		t.Fatalf("管理员访问自己的文件应 403 admin_no_storage，实际 %d %s", r.status, r.body)
	}

	// 5. 缺少二次密码时不能创建用户
	r = e.json("POST", "/api/users", map[string]any{
		"username": "alice", "password": "alice12345", "root": "alice", "quota": 10 << 30,
	})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("缺少二次密码应 428，实际 %d %s", r.status, r.body)
	}

	// 6. 密码错误时被拒绝
	r = e.json("POST", "/api/users", map[string]any{
		"username": "alice", "password": "alice12345", "root": "alice", "adminPassword": "wrong",
	})
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "reauth_failed") {
		t.Fatalf("二次密码错误应 403 reauth_failed，实际 %d %s", r.status, r.body)
	}

	// 7. 正确密码创建普通用户
	created := e.mustJSON("POST", "/api/users", map[string]any{
		"username": "alice", "password": "alice12345", "displayName": "爱丽丝",
		"root": "alice", "quota": 10 << 30, "rateLimitMbps": 0,
		"adminPassword": "admin",
	}, http.StatusOK)
	if created["user"].(map[string]any)["root"] != "alice" {
		t.Fatalf("用户存储空间不正确：%v", created)
	}

	// 8. 普通用户登录
	e.logout()
	e.loginAs("alice", "alice12345")
	me = e.mustJSON("GET", "/api/auth/me", nil, http.StatusOK)
	if me["user"].(map[string]any)["isAdmin"] != false {
		t.Fatal("alice 不应是管理员")
	}
	if home, _ := me["home"].(string); home != "/" {
		t.Fatalf("普通用户 home 应为 /，实际 %q", home)
	}

	// 9. 建目录
	e.mustJSON("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "docs"}, http.StatusOK)
	list := e.mustJSON("GET", "/api/fs/list?path=", nil, http.StatusOK)
	if len(list["entries"].([]any)) != 1 {
		t.Fatalf("应有 1 个条目：%v", list)
	}

	// 10. 简单上传
	payload := []byte("hello homevault 你好\n")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "笔记.txt")
	fw.Write(payload)
	mw.Close()
	r = e.do("POST", "/api/fs/upload?path=docs", buf.Bytes(),
		map[string]string{"Content-Type": mw.FormDataContentType()})
	if r.status != http.StatusOK {
		t.Fatalf("上传失败：%d %s", r.status, r.body)
	}
	list = e.mustJSON("GET", "/api/fs/list?path=docs", nil, http.StatusOK)
	entries := list["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("docs 下应有 1 个文件：%v", list)
	}
	first := entries[0].(map[string]any)
	if first["name"] != "笔记.txt" || first["kind"] != "text" {
		t.Fatalf("上传结果字段不正确：%v", first)
	}

	// 11. 下载并比对内容
	dl := e.json("GET", "/api/fs/download?path=docs/%E7%AC%94%E8%AE%B0.txt&download=1", nil)
	if dl.status != http.StatusOK || !bytes.Equal(dl.body, payload) {
		t.Fatalf("下载内容不一致：%d %q", dl.status, dl.body)
	}
	// Range 支持
	r = e.do("GET", "/api/fs/download?path=docs/%E7%AC%94%E8%AE%B0.txt",
		nil, map[string]string{"Range": "bytes=0-4"})
	if r.status != http.StatusPartialContent || string(r.body) != "hello" {
		t.Fatalf("Range 下载异常：%d %q", r.status, r.body)
	}

	// 12. 分片上传（续传语义）
	big := bytes.Repeat([]byte("0123456789"), 100) // 1000 字节
	sess := e.mustJSON("POST", "/api/upload/init", map[string]any{
		"dir": "", "name": "big.bin", "size": len(big),
	}, http.StatusOK)
	id := sess["id"].(string)
	if sess["chunkCount"].(float64) != 1 {
		t.Fatalf("分片数应为 1：%v", sess)
	}
	// 再次 init 应当复用同一会话（断点续传）
	again := e.mustJSON("POST", "/api/upload/init", map[string]any{
		"dir": "", "name": "big.bin", "size": len(big),
	}, http.StatusOK)
	if again["id"] != id {
		t.Fatalf("重复 init 应返回同一会话：%v vs %v", again["id"], id)
	}
	// 长度不符应当报错
	r = e.do("PUT", "/api/upload/"+id+"/0", big[:10], nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("分片长度不符应 400，实际 %d %s", r.status, r.body)
	}
	// 正确上传
	r = e.do("PUT", "/api/upload/"+id+"/0", big, nil)
	if r.status != http.StatusOK {
		t.Fatalf("上传分片失败：%d %s", r.status, r.body)
	}
	// 重复上传同一分片应幂等
	r = e.do("PUT", "/api/upload/"+id+"/0", big, nil)
	if r.status != http.StatusOK {
		t.Fatalf("重复分片应幂等：%d %s", r.status, r.body)
	}
	done := e.mustJSON("POST", "/api/upload/"+id+"/complete", map[string]any{}, http.StatusOK)
	if done["name"] != "big.bin" || int64(done["size"].(float64)) != int64(len(big)) {
		t.Fatalf("合并结果不正确：%v", done)
	}
	dl = e.json("GET", "/api/fs/download?path=big.bin&download=1", nil)
	if !bytes.Equal(dl.body, big) {
		t.Fatalf("分片合并后的内容不一致（%d 字节）", len(dl.body))
	}
	// 会话应已被清理
	r = e.json("GET", "/api/upload/"+id, nil)
	if r.status != http.StatusNotFound {
		t.Fatalf("完成后会话应不存在，实际 %d", r.status)
	}

	// 13. 搜索与聚合
	search := e.mustJSON("GET", "/api/search?q=big&includeDirs=1", nil, http.StatusOK)
	if search["total"].(float64) < 1 {
		t.Fatalf("搜索应命中 big.bin：%v", search)
	}
	if len(search["byKind"].([]any)) == 0 {
		t.Fatalf("搜索应返回聚合信息：%v", search)
	}

	// 14. 重命名 / 复制 / 移动
	e.mustJSON("POST", "/api/fs/rename", map[string]any{"path": "big.bin", "newName": "big2.bin"}, http.StatusOK)
	e.mustJSON("POST", "/api/fs/copy", map[string]any{"paths": []string{"big2.bin"}, "dest": "docs"}, http.StatusOK)
	docsList := e.mustJSON("GET", "/api/fs/list?path=docs", nil, http.StatusOK)
	copiedName := ""
	for _, raw := range docsList["entries"].([]any) {
		ent := raw.(map[string]any)
		if ent["name"] != "笔记.txt" {
			copiedName = ent["name"].(string)
		}
	}
	if copiedName == "" {
		t.Fatalf("复制到 docs 后应出现副本：%v", docsList)
	}
	e.mustJSON("POST", "/api/fs/move", map[string]any{
		"paths": []string{"docs/" + copiedName}, "dest": "",
	}, http.StatusOK)
	// 在根目录再复制一份同名文件：必须自动改名，绝不能覆盖已有文件
	e.mustJSON("POST", "/api/fs/copy", map[string]any{"paths": []string{"big2.bin"}, "dest": ""}, http.StatusOK)
	rootList := e.mustJSON("GET", "/api/fs/list?path=", nil, http.StatusOK)
	names := map[string]bool{}
	for _, raw := range rootList["entries"].([]any) {
		names[raw.(map[string]any)["name"].(string)] = true
	}
	if !names["big2.bin"] || !names[copiedName] || !names["docs"] {
		t.Fatalf("根目录里应同时存在 big2.bin、副本 %q 与 docs：%v", copiedName, names)
	}
	// 两次复制会生成两个不同名的副本，绝不允许覆盖
	if len(names) != 4 {
		t.Fatalf("根目录应有 docs/big2.bin/两个副本 共四项，实际 %v", names)
	}

	// 15. 删除到回收站；普通账号现在可管理"自己"的回收站（按账号隔离）
	e.mustJSON("POST", "/api/fs/delete", map[string]any{"paths": []string{"docs/笔记.txt"}}, http.StatusOK)
	// 普通账号能看自己的回收站，且只能看到自己的条目
	trash := e.mustJSON("GET", "/api/trash", nil, http.StatusOK)
	items := trash["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("回收站应有 1 项（alice 自己的）：%v", trash)
	}
	trashID := items[0].(map[string]any)["id"].(string)
	// alice 自己清空/恢复不需要管理员密码，只验证权限位（这里直接用 alice 登录态）
	restore := e.mustJSON("POST", "/api/trash/restore", map[string]any{
		"ids": []string{trashID}, "password": "alice12345",
	}, http.StatusOK)
	if len(restore["restored"].([]any)) != 1 {
		t.Fatalf("恢复失败：%v", restore)
	}
	list = e.mustJSON("GET", "/api/fs/list?path=docs", nil, http.StatusOK)
	if len(list["entries"].([]any)) != 1 {
		t.Fatalf("恢复后 docs 应有 1 个文件：%v", list)
	}
	// 管理员仍可按账号代操作：删除再列回收站并恢复
	e.mustJSON("POST", "/api/fs/delete", map[string]any{"paths": []string{"docs/笔记.txt"}}, http.StatusOK)
	e.loginAs("admin", "admin")
	trash = e.mustJSON("GET", "/api/trash?user=alice", nil, http.StatusOK)
	items = trash["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("管理员代查回收站应有 1 项：%v", trash)
	}
	trashID = items[0].(map[string]any)["id"].(string)
	restore = e.mustJSON("POST", "/api/trash/restore?user=alice", map[string]any{
		"ids": []string{trashID}, "password": "admin",
	}, http.StatusOK)
	if len(restore["restored"].([]any)) != 1 {
		t.Fatalf("管理员代恢复失败：%v", restore)
	}
	e.loginAs("alice", "alice12345")
	list = e.mustJSON("GET", "/api/fs/list?path=docs", nil, http.StatusOK)
	if len(list["entries"].([]any)) != 1 {
		t.Fatalf("代恢复后 docs 应有 1 个文件：%v", list)
	}

	// 16. 审计日志：普通账号只能看自己的记录（按账号隔离，不再 403）
	logs := e.mustJSON("GET", "/api/logs?limit=50", nil, http.StatusOK)
	if logs["total"].(float64) < 1 {
		t.Fatalf("alice 应能看到自己（>=1 条）日志：%v", logs)
	}
	e.loginAs("admin", "admin")
	allLogs := e.mustJSON("GET", "/api/logs?limit=50", nil, http.StatusOK)
	if allLogs["total"].(float64) < 1 {
		t.Fatalf("管理员应能看到日志：%v", allLogs)
	}

	// 17. 配额：把 alice 的配额改成 1KiB，再上传 2KiB 应被拒绝（507）
	e.logout()
	e.loginAs("admin", "admin")
	e.mustJSON("PATCH", "/api/users/alice", map[string]any{
		"quota": 1024, "adminPassword": "admin",
	}, http.StatusOK)
	// 管理员代操作：写操作必须重新给密码
	r = e.json("POST", "/api/fs/mkdir?user=alice", map[string]any{"dir": "", "name": "hack"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("管理员代操作缺少密码应 428，实际 %d %s", r.status, r.body)
	}
	r = e.json("POST", "/api/fs/mkdir?user=alice", map[string]any{
		"dir": "", "name": "admins-made", "password": "admin",
	})
	if r.status != http.StatusOK {
		t.Fatalf("管理员带密码代操作应成功：%d %s", r.status, r.body)
	}
	// 管理员代办要留下 actingAs 审计（actingAs = 被操作的目标账号）
	auditLogs := e.mustJSON("GET", "/api/logs?action=mkdir&limit=20", nil, http.StatusOK)
	foundActing := false
	for _, raw := range auditLogs["entries"].([]any) {
		if raw.(map[string]any)["actingAs"] == "alice" {
			foundActing = true
		}
	}
	if !foundActing {
		t.Fatalf("审计日志应包含 actingAs=alice：%v", auditLogs["entries"])
	}
	// 超配额写入
	var qbuf bytes.Buffer
	qw := multipart.NewWriter(&qbuf)
	qf, _ := qw.CreateFormFile("file", "too-big.bin")
	qf.Write(bytes.Repeat([]byte("x"), 4096))
	qw.Close()
	r = e.do("POST", "/api/fs/upload?path=&user=alice", qbuf.Bytes(), map[string]string{
		"Content-Type":      qw.FormDataContentType(),
		"X-LD-Password-B64": b64("admin"),
	})
	if r.status != http.StatusInsufficientStorage {
		t.Fatalf("超配额应 507，实际 %d %s", r.status, r.body)
	}

	// 18. 设置：修改默认配额并立即生效
	settings := e.mustJSON("GET", "/api/settings", nil, http.StatusOK)
	if settings["settings"] == nil || settings["effective"] == nil {
		t.Fatalf("设置响应缺字段：%v", settings)
	}
	patched := e.mustJSON("PATCH", "/api/settings", map[string]any{
		"defaultQuota": "2GiB", "adminPassword": "admin",
	}, http.StatusOK)
	next := patched["settings"].(map[string]any)
	if int64(next["defaultQuota"].(float64)) != 2<<30 {
		t.Fatalf("默认配额未生效：%v", next["defaultQuota"])
	}
	// 非法设置项要被拒绝且不会静默生效
	r = e.json("PATCH", "/api/settings", map[string]any{"chunkSize": "abc", "adminPassword": "admin"})
	if r.status != http.StatusBadRequest {
		t.Fatalf("非法分片大小应 400，实际 %d %s", r.status, r.body)
	}
	// 未知字段要报错
	r = e.json("PATCH", "/api/settings", map[string]any{"nonsense": 1, "adminPassword": "admin"})
	if r.status != http.StatusBadRequest {
		t.Fatalf("未知设置项应 400，实际 %d %s", r.status, r.body)
	}

	// 19. 最后一个管理员不能删除/降级自己
	r = e.json("DELETE", "/api/users/admin", nil)
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("删除管理员缺少二次密码应 428，实际 %d %s", r.status, r.body)
	}
	r = e.do("DELETE", "/api/users/admin", nil, map[string]string{"X-LD-Password-B64": b64("admin")})
	if r.status != http.StatusBadRequest {
		t.Fatalf("删除自己应 400，实际 %d %s", r.status, r.body)
	}

	// 20. 强制改凭据：新建用户后登录，除改凭据外全部被拦截
	e.mustJSON("POST", "/api/users", map[string]any{
		"username": "bob", "password": "bob1234567", "root": "bob", "adminPassword": "admin",
	}, http.StatusOK)
	bob, _ := e.store.User("bob")
	cp := bob.Clone()
	cp.MustChangeCredentials = true
	if err := e.store.PutUser(cp); err != nil {
		t.Fatal(err)
	}
	e.logout()
	e.loginAs("bob", "bob1234567")
	r = e.json("GET", "/api/fs/list", nil)
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "must_change_credentials") {
		t.Fatalf("未改凭据时应 403 must_change_credentials，实际 %d %s", r.status, r.body)
	}
	e.mustJSON("POST", "/api/auth/change-credentials", map[string]any{
		"currentPassword": "bob1234567", "newUsername": "bobby", "newPassword": "bobby123456",
	}, http.StatusOK)
	// 改名后旧会话失效、新会话可用
	list = e.mustJSON("GET", "/api/fs/list", nil, http.StatusOK)
	if list["path"] != "" {
		t.Fatalf("改凭据后应能正常列目录：%v", list)
	}
	if _, ok := e.store.User("bob"); ok {
		t.Fatal("改名后不应还存在 bob")
	}
	if _, ok := e.store.User("bobby"); !ok {
		t.Fatal("改名后应存在 bobby")
	}

	// 21. 改密码后旧密码不能登录
	e.logout()
	r = e.json("POST", "/api/auth/login", map[string]string{"username": "bobby", "password": "bob1234567"})
	if r.status != http.StatusUnauthorized {
		t.Fatalf("旧密码应登录失败，实际 %d", r.status)
	}
	e.loginAs("bobby", "bobby123456")

	// 22. 路径逃逸与符号链接必须被拒绝
	e.logout()
	e.loginAs("alice", "alice12345")
	for _, bad := range []string{"../secret", "..%2Fsecret", "/etc/passwd", "../../../etc/passwd"} {
		r = e.json("GET", "/api/fs/list?path="+bad, nil)
		if r.status == http.StatusOK {
			t.Fatalf("危险的路径 %q 不应被接受", bad)
		}
	}
	// ".." 会被规范化掉而不是逃逸：a/../../b 等价于 b
	e.mustJSON("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "b"}, http.StatusOK)
	norm := e.mustJSON("GET", "/api/fs/list?path=a/../../b", nil, http.StatusOK)
	if norm["path"] != "b" {
		t.Fatalf("路径规范化结果不正确：%v", norm["path"])
	}
	// 符号链接指向空间外时必须拒绝
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	aliceRoot, err := e.svc.FS.EnsureRoot("alice")
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(aliceRoot, "link")
	_ = os.Remove(link)
	if err := os.Symlink(outside, link); err == nil {
		r = e.json("GET", "/api/fs/list?path=link", nil)
		if r.status == http.StatusOK {
			t.Fatalf("符号链接不应被跟随：%d %s", r.status, r.body)
		}
	}

	// 23. 管理员端与用户端入口都必须能返回页面
	for _, p := range []string{"/", "/admin/"} {
		res, err := http.Get(e.baseURL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html>") {
			t.Fatalf("入口 %s 异常：%d %s", p, res.StatusCode, body)
		}
	}
	// 未知 API 返回 JSON 404
	r = e.json("GET", "/api/nope", nil)
	if r.status != http.StatusNotFound || !strings.Contains(string(r.body), "not_found") {
		t.Fatalf("未知接口应 404 JSON，实际 %d %s", r.status, r.body)
	}

	// 24. 空间目录布局符合约定
	for _, name := range []string{"files", "trash", "audit", "uploads", "cache", "state.json"} {
		if _, err := os.Stat(filepath.Join(e.cfg.SpaceDir, name)); err != nil {
			t.Fatalf("空间目录缺少 %s：%v", name, err)
		}
	}
	_ = fmt.Sprint()
}
