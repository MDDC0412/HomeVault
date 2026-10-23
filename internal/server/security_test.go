package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"homevault/internal/audit"
)

// 本文件是"对抗性"验证：假设攻击者已经拿到普通用户会话，检查能否越权读到别人的空间、
// 绕过二次密码、绕过配额、或在改密码后继续使用旧会话。

// regression：这些用例原本用于复现已知缺陷；缺陷修复后改为默认运行的回归用例，id/desc 只用于回溯。
func regression(t *testing.T, id, desc string) {
	t.Helper()
	// 该缺陷已修复，用例默认运行；id/desc 仅用于回溯历史。
	t.Logf("回归用例 %s：%s", id, desc)
}

// api 是一个独立的浏览器会话（自己的 Cookie jar），用于同时扮演多个用户。
type api struct {
	t    *testing.T
	e    *env
	c    *http.Client
	base string
}

func (e *env) newAPI() *api {
	jar, _ := cookiejar.New(nil)
	return &api{t: e.t, e: e, base: e.baseURL,
		c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (a *api) do(method, path string, body any, headers map[string]string) resp {
	a.t.Helper()
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
			a.t.Fatalf("序列化请求失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatalf("构造请求失败：%v", err)
	}
	if body != nil && reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s 请求失败：%v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: raw, header: res.Header}
}

func (a *api) json(method, path string, body any) resp {
	return a.do(method, path, body, nil)
}

func (a *api) must(method, path string, body any, want int) map[string]any {
	a.t.Helper()
	r := a.json(method, path, body)
	if r.status != want {
		a.t.Fatalf("%s %s 期望 %d，实际 %d：%s", method, path, want, r.status, string(r.body))
	}
	if len(r.body) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		a.t.Fatalf("%s %s 响应不是 JSON：%s", method, path, string(r.body))
	}
	return out
}

func (a *api) login(user, password string) {
	a.t.Helper()
	a.must("POST", "/api/auth/login", map[string]string{"username": user, "password": password}, http.StatusOK)
}

// token 取出当前会话的 Cookie 值，用于稍后"重放"旧会话。
func (a *api) token() string {
	a.t.Helper()
	u, err := url.Parse(a.base)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, c := range a.c.Jar.Cookies(u) {
		if c.Name == "homevault_session" {
			return c.Value
		}
	}
	a.t.Fatal("会话 Cookie 不存在")
	return ""
}

func (e *env) code(resp resp) string {
	var m map[string]any
	_ = json.Unmarshal(resp.body, &m)
	s, _ := m["code"].(string)
	return s
}

type uploadReq struct {
	body    []byte
	headers map[string]string
}

// multipartUpload 构造一个简单的 multipart/form-data 上传请求体。
func multipartUpload(t *testing.T, filename string, payload []byte) uploadReq {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return uploadReq{body: buf.Bytes(), headers: map[string]string{"Content-Type": mw.FormDataContentType()}}
}

// setupAliceBob 用管理员会话创建两个普通用户并返回二者各自的会话。
func setupAliceBob(t *testing.T, e *env) (*api, *api) {
	t.Helper()
	admin := e.newAPI()
	admin.login("admin", "admin")
	for _, u := range []struct{ name, pw string }{{"alice", "alice12345"}, {"bob", "bob1234567"}} {
		admin.must("POST", "/api/users", map[string]any{
			"username": u.name, "password": u.pw, "root": u.name,
			"quota": int64(10 << 30), "adminPassword": "admin",
		}, http.StatusOK)
	}
	alice, bob := e.newAPI(), e.newAPI()
	alice.login("alice", "alice12345")
	bob.login("bob", "bob1234567")
	return alice, bob
}

func TestNonAdminCannotUseUserParam(t *testing.T) {
	e := newEnv(t)
	alice, bob := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "secret"}, http.StatusOK)

	for _, path := range []string{
		"/api/fs/list?user=alice",
		"/api/fs/list?user=alice&path=",
		"/api/fs/download?user=alice&path=secret",
		"/api/search?user=alice&q=x",
		"/api/trash?user=alice",
		"/api/users?user=alice",
		"/api/logs?user=alice",
		"/api/upload?user=alice",
		"/api/preview?user=alice&path=secret",
	} {
		r := bob.json("GET", path, nil)
		if r.status != http.StatusForbidden {
			t.Fatalf("普通用户带 ?user= 应 403，实际 %d %s（%s）", r.status, r.body, path)
		}
		if e.code(r) != "forbidden" {
			t.Fatalf("应返回 forbidden，实际 %s（%s）", e.code(r), path)
		}
	}
	// 写操作同样被拒
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/fs/mkdir?user=alice", map[string]any{"dir": "", "name": "x"}},
		{"POST", "/api/fs/delete?user=alice", map[string]any{"paths": []string{"secret"}}},
		{"POST", "/api/upload/init?user=alice", map[string]any{"dir": "", "name": "x", "size": 1}},
		{"DELETE", "/api/upload/whatever?user=alice", nil},
	} {
		r := bob.json(c.method, c.path, c.body)
		if r.status != http.StatusForbidden {
			t.Fatalf("%s %s 应 403，实际 %d %s", c.method, c.path, r.status, r.body)
		}
	}
	// 管理员指定不存在的用户 → 404
	admin := e.newAPI()
	admin.login("admin", "admin")
	r := admin.json("GET", "/api/fs/list?user=nobody", nil)
	if r.status != http.StatusNotFound || e.code(r) != "user_not_found" {
		t.Fatalf("管理员指定不存在的用户应 404 user_not_found，实际 %d %s", r.status, r.body)
	}
	// 普通用户把 user 写成自己 → 同样拒绝：?user= 是管理员专属参数，
	// 不能成为"带个参数就绕过权限位"的口子。
	if r := bob.json("GET", "/api/fs/list?user=bob&path=", nil); r.status != http.StatusForbidden {
		t.Fatalf("user=自己 也应 403，实际 %d %s", r.status, r.body)
	}
	// 不带 user 时一切正常
	if r := bob.json("GET", "/api/fs/list?path=", nil); r.status != http.StatusOK {
		t.Fatalf("普通用户不带 user 应正常工作，实际 %d %s", r.status, r.body)
	}
}

func TestCrossRootIsolationPaths(t *testing.T) {
	e := newEnv(t)
	alice, bob := setupAliceBob(t, e)
	const marker = "TOP-SECRET-ALICE-CONTENT"
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "secret"}, http.StatusOK)
	up := multipartUpload(t, "note.txt", []byte(marker))
	if r := alice.do("POST", "/api/fs/upload?path=secret", up.body, up.headers); r.status != http.StatusOK {
		t.Fatalf("alice 上传失败：%d %s", r.status, r.body)
	}

	// 各种越界写法都必须拿不到 alice 的内容
	for _, p := range []string{
		"../alice/secret/note.txt",
		"..%2Falice%2Fsecret%2Fnote.txt",
		"%2E%2E%2Falice%2Fsecret%2Fnote.txt",
		"..\\alice\\secret\\note.txt",
		"....//alice/secret/note.txt",
		"../ALICE/secret/note.txt",
		"../Alice/secret/note.txt",
		"../ALICE~1/secret/note.txt",
		"../alice./secret/note.txt",
		"../alice%20/secret/note.txt",
		"/alice/secret/note.txt",
		"//alice/secret/note.txt",
		`\\localhost\c$\alice\secret\note.txt`,
		"se\x00cret/note.txt",
	} {
		enc := url.QueryEscape(p)
		for _, ep := range []string{
			"/api/fs/download?path=" + enc,
			"/api/fs/list?path=" + enc,
			"/api/preview?path=" + enc,
		} {
			r := bob.json("GET", ep, nil)
			if strings.Contains(string(r.body), marker) {
				t.Fatalf("bob 通过 %q 读到了 alice 的内容：%d %s", p, r.status, r.body)
			}
			if r.status == http.StatusOK {
				// 200 只允许出现在"解析到自己空间内的某个不存在路径"之外的情况，这里全部不允许
				var m map[string]any
				_ = json.Unmarshal(r.body, &m)
				if _, ok := m["entries"]; ok {
					t.Fatalf("bob 通过 %q 成功列出了目录：%s", p, r.body)
				}
			}
		}
	}
	// 直接列 bob 自己的根，确认隔离是"看不到"而不是"服务坏了"
	list := bob.must("GET", "/api/fs/list?path=", nil, http.StatusOK)
	if entries, _ := list["entries"].([]any); len(entries) != 0 {
		t.Fatalf("bob 的根目录应为空，实际 %v", list)
	}
}

func TestAdminWithoutTargetHasNoStorage(t *testing.T) {
	e := newEnv(t)
	admin := e.newAPI()
	admin.login("admin", "admin")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/fs/list?path=", nil},
		{"GET", "/api/fs/download?path=x", nil},
		{"POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "x", "password": "admin"}},
		{"POST", "/api/upload/init", map[string]any{"dir": "", "name": "x", "size": 1, "password": "admin"}},
		{"GET", "/api/search?q=x", nil},
	} {
		r := admin.json(c.method, c.path, c.body)
		if r.status != http.StatusForbidden || e.code(r) != "admin_no_storage" {
			t.Fatalf("%s %s 应 403 admin_no_storage，实际 %d %s", c.method, c.path, r.status, r.body)
		}
	}
	// 回收站/日志/用户列表不需要自己的存储空间，管理员不带 ?user= 也能看（且看到的是全部）
	if r := admin.json("GET", "/api/trash", nil); r.status != http.StatusOK {
		t.Fatalf("管理员不带 ?user= 查看回收站应 200，实际 %d %s", r.status, r.body)
	}
	if r := admin.json("GET", "/api/logs", nil); r.status != http.StatusOK {
		t.Fatalf("管理员不带 ?user= 查看日志应 200，实际 %d %s", r.status, r.body)
	}
	// 带上 ?user= 后可以只读访问
	alice, _ := setupAliceBob(t, e)
	admin.must("GET", "/api/fs/list?user=alice&path=", nil, http.StatusOK)
	_ = alice
}

func TestAdminWriteOpsRequireSecondPassword(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)

	admin := e.newAPI()
	admin.login("admin", "admin")

	// 1) 完全不给密码 → 428
	r := admin.json("POST", "/api/fs/mkdir?user=alice", map[string]any{"dir": "", "name": "x"})
	if r.status != http.StatusPreconditionRequired || e.code(r) != "reauth_required" {
		t.Fatalf("缺少二次密码应 428 reauth_required，实际 %d %s", r.status, r.body)
	}
	// 2) 密码错误 → 403 reauth_failed
	r = admin.json("POST", "/api/fs/mkdir?user=alice",
		map[string]any{"dir": "", "name": "x", "password": "wrong"})
	if r.status != http.StatusForbidden || e.code(r) != "reauth_failed" {
		t.Fatalf("二次密码错误应 403 reauth_failed，实际 %d %s", r.status, r.body)
	}
	// 2b) 文件类接口只认 password 字段，写成 adminPassword 会被当成未知字段拒绝
	r = admin.json("POST", "/api/fs/mkdir?user=alice",
		map[string]any{"dir": "", "name": "x", "adminPassword": "admin"})
	if r.status != http.StatusBadRequest {
		t.Fatalf("文件接口的二次密码字段名是 password，实际 %d %s", r.status, r.body)
	}
	// 3) 请求体 password 字段 → 通过
	admin.must("POST", "/api/fs/mkdir?user=alice", map[string]any{"dir": "", "name": "ok1", "password": "admin"}, http.StatusOK)
	// 4) X-LD-Password 头 → 通过
	r = admin.do("POST", "/api/fs/mkdir?user=alice", map[string]any{"dir": "", "name": "ok2"},
		map[string]string{"X-LD-Password": "admin"})
	if r.status != http.StatusOK {
		t.Fatalf("X-LD-Password 头应被接受，实际 %d %s", r.status, r.body)
	}
	// 5) X-LD-Password-B64 → 通过（且优先于明文头）
	r = admin.do("POST", "/api/fs/mkdir?user=alice", map[string]any{"dir": "", "name": "ok3"},
		map[string]string{"X-LD-Password": "wrong", "X-LD-Password-B64": b64("admin")})
	if r.status != http.StatusOK {
		t.Fatalf("X-LD-Password-B64 应被接受，实际 %d %s", r.status, r.body)
	}
	// 6) 删除、重命名、复制都要二次密码
	r = admin.json("POST", "/api/fs/delete?user=alice", map[string]any{"paths": []string{"ok1"}})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("删除需要二次密码，实际 %d %s", r.status, r.body)
	}
	r = admin.json("POST", "/api/fs/rename?user=alice", map[string]any{"path": "ok2", "newName": "ok3"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("重命名需要二次密码，实际 %d %s", r.status, r.body)
	}
	// 7) 操作自己的文件（管理员没有空间，用普通用户验证语义）
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "own"}, http.StatusOK)
	// 8) 配额不足与二次密码的顺序：先校验密码
	r = admin.json("POST", "/api/fs/delete?user=alice", map[string]any{"paths": []string{"nope"}})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("删不存在的条目也应先要密码，实际 %d %s", r.status, r.body)
	}
}

func TestSessionDiesAfterUsernameChange(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	stolen := alice.token()
	// 改名（同时改密码，触发 rename 路径）
	r := alice.json("POST", "/api/auth/change-credentials", map[string]any{
		"currentPassword": "alice12345", "newUsername": "alice2", "newPassword": "alice23456",
	})
	if r.status != http.StatusOK {
		t.Fatalf("改名失败：%d %s", r.status, r.body)
	}
	// 旧令牌里写的是旧账号名，改名后必须失效
	replay := e.newAPI()
	rr := replay.do("GET", "/api/auth/me", nil, map[string]string{"Authorization": "Bearer " + stolen})
	if rr.status != http.StatusUnauthorized {
		t.Fatalf("改名后旧令牌必须 401，实际 %d %s", rr.status, rr.body)
	}
	// 新账号可用，旧账号不可用
	fresh := e.newAPI()
	fresh.login("alice2", "alice23456")
	old := e.newAPI()
	if r := old.json("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "alice23456"}); r.status != http.StatusUnauthorized {
		t.Fatalf("旧账号名应登录失败，实际 %d %s", r.status, r.body)
	}
}

// 改密码必须让其它会话立即失效（docs/API.md 与 auth 包的文档都这么承诺）。
func TestOldSessionDiesAfterPasswordChange(t *testing.T) {
	regression(t, "#S1",
		"会话令牌是 base64(payload)+HMAC，payload 里的 passwordVersion 只在签发时写入"+
			"（server.go:279），authenticate（server.go:253-275）只调 Tokens.Verify 拿用户名，"+
			"从不与用户当前的 PasswordHash 比对；因此改密码/被管理员重置密码后，旧令牌依然有效")
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	stolen := alice.token()
	// 管理员重置 alice 的密码
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("PATCH", "/api/users/alice", map[string]any{
		"newPassword": "brandnew9876", "adminPassword": "admin"}, http.StatusOK)

	replay := e.newAPI()
	rr := replay.do("GET", "/api/auth/me", nil, map[string]string{"Authorization": "Bearer " + stolen})
	if rr.status != http.StatusUnauthorized {
		t.Fatalf("管理员重置密码后，旧令牌必须失效，实际 %d %s", rr.status, rr.body)
	}
}

// 自助改密码同样必须让旧令牌失效。
func TestOldSessionDiesAfterSelfPasswordChange(t *testing.T) {
	regression(t, "#S2",
		"handleChangeCredentials（handlers_auth.go:202）用新哈希重新签发了当前会话的 Cookie，"+
			"但没有让其它会话失效；passwordVersion 机制没有被任何地方校验")
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	stolen := alice.token()
	r := alice.json("POST", "/api/auth/change-credentials", map[string]any{
		"currentPassword": "alice12345", "newPassword": "alice23456"})
	if r.status != http.StatusOK {
		t.Fatalf("改密码失败：%d %s", r.status, r.body)
	}
	replay := e.newAPI()
	rr := replay.do("GET", "/api/auth/me", nil, map[string]string{"Authorization": "Bearer " + stolen})
	if rr.status != http.StatusUnauthorized {
		t.Fatalf("自行改密码后，旧令牌必须失效，实际 %d %s", rr.status, rr.body)
	}
}

func TestDisabledAccountRejectedEverywhere(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	admin := e.newAPI()
	admin.login("admin", "admin")
	if r := admin.json("PATCH", "/api/users/alice", map[string]any{"disabled": true, "adminPassword": "admin"}); r.status != http.StatusOK {
		t.Fatalf("禁用失败：%d %s", r.status, r.body)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/auth/me", nil},
		{"GET", "/api/fs/list?path=", nil},
		{"GET", "/api/search?q=x", nil},
		{"POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "x"}},
		{"GET", "/api/trash", nil},
		{"GET", "/api/logs", nil},
		{"POST", "/api/upload/init", map[string]any{"dir": "", "name": "x", "size": 1}},
	} {
		r := alice.json(c.method, c.path, c.body)
		if r.status != http.StatusForbidden || e.code(r) != "disabled" {
			t.Fatalf("禁用账号 %s %s 应 403 disabled，实际 %d %s", c.method, c.path, r.status, r.body)
		}
	}
	// 登录也被拒
	fresh := e.newAPI()
	r := fresh.json("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "alice12345"})
	if r.status != http.StatusForbidden || e.code(r) != "disabled" {
		t.Fatalf("禁用账号登录应 403 disabled，实际 %d %s", r.status, r.body)
	}
	// 重新启用后恢复
	admin.must("PATCH", "/api/users/alice", map[string]any{"disabled": false, "adminPassword": "admin"}, http.StatusOK)
	alice.must("GET", "/api/fs/list?path=", nil, http.StatusOK)
}

func TestDownloadWithFlagIsAudited(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	up := multipartUpload(t, "a.txt", []byte("hello"))
	if r := alice.do("POST", "/api/fs/upload?path=d", up.body, up.headers); r.status != http.StatusOK {
		t.Fatalf("上传失败：%d %s", r.status, r.body)
	}
	if r := alice.json("GET", "/api/fs/download?path=d/a.txt&download=1", nil); r.status != http.StatusOK {
		t.Fatalf("下载失败：%d %s", r.status, r.body)
	}
	res, err := e.svc.Logs.Query(audit.Query{Action: audit.ActionDownload, User: "alice"})
	if err != nil || res.Total != 1 {
		t.Fatalf("带 download=1 应写 1 条下载审计，实际 %d err=%v", res.Total, err)
	}
	if res.Entries[0].Target != "d/a.txt" {
		t.Fatalf("审计目标不正确：%+v", res.Entries[0])
	}
}

// 任何一次真实的文件下载都应留痕，而不是取决于客户端是否传 download=1。
func TestDownloadIsAlwaysAudited(t *testing.T) {
	regression(t, "#S3",
		"handleDownload（handlers_fs.go:128-130）只有 query 里带 download=1 才写审计；"+
			"自带前端（web/app.js）在预览/内联打开时不带该参数，因此大量真实读取不留痕")
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	up := multipartUpload(t, "a.txt", []byte("hello"))
	if r := alice.do("POST", "/api/fs/upload?path=d", up.body, up.headers); r.status != http.StatusOK {
		t.Fatalf("上传失败：%d %s", r.status, r.body)
	}
	if r := alice.json("GET", "/api/fs/download?path=d/a.txt", nil); r.status != http.StatusOK {
		t.Fatalf("下载失败：%d %s", r.status, r.body)
	}
	res, err := e.svc.Logs.Query(audit.Query{Action: audit.ActionDownload, User: "alice"})
	if err != nil || res.Total < 1 {
		t.Fatalf("读取文件必须留痕，实际 %d 条 err=%v", res.Total, err)
	}
}

// 内联预览（inline=1）同样是读取文件，必须留痕。
func TestInlinePreviewIsAudited(t *testing.T) {
	regression(t, "#S4", "内联读取走的是同一个 handleDownload，只有 download=1 才写审计")
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	up := multipartUpload(t, "a.txt", []byte("hello"))
	if r := alice.do("POST", "/api/fs/upload?path=d", up.body, up.headers); r.status != http.StatusOK {
		t.Fatalf("上传失败：%d %s", r.status, r.body)
	}
	if r := alice.json("GET", "/api/fs/download?path=d/a.txt&inline=1", nil); r.status != http.StatusOK {
		t.Fatalf("内联读取失败：%d %s", r.status, r.body)
	}
	res, _ := e.svc.Logs.Query(audit.Query{Action: audit.ActionDownload, User: "alice"})
	if res.Total < 1 {
		t.Fatalf("内联预览必须留痕，实际 %d 条", res.Total)
	}
}

func TestAdminAccessIsAuditedWithTarget(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("GET", "/api/fs/list?user=alice&path=", nil, http.StatusOK)

	res, err := e.svc.Logs.Query(audit.Query{Action: audit.ActionAdminAccess, User: "admin"})
	if err != nil || res.Total < 1 {
		t.Fatalf("管理员访问他人文件应写审计，实际 %d err=%v", res.Total, err)
	}
	if res.Entries[0].Target != "alice" {
		t.Fatalf("审计 Target 应为被操作用户 alice，实际 %q", res.Entries[0].Target)
	}
}

// ActingAs 字段的语义是"被操作的账号"，但实现写的是操作者自己。
func TestAdminAccessActingAsIsTargetUser(t *testing.T) {
	regression(t, "#S5",
		"server.go:236 把 asWho 赋成 actor.Username（管理员自己），随后 audit.Entry.ActingAs "+
			"写的就是管理员名；core.Actor.ActingAs 的注释与 docs/API.md 都称它是\"被操作的账号\"")
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("GET", "/api/fs/list?user=alice&path=", nil, http.StatusOK)

	res, err := e.svc.Logs.Query(audit.Query{Action: audit.ActionAdminAccess, User: "admin"})
	if err != nil || res.Total < 1 {
		t.Fatalf("缺少审计记录：%d %v", res.Total, err)
	}
	if res.Entries[0].ActingAs != "alice" {
		t.Fatalf("ActingAs 应记录被操作的账号 alice，实际 %q", res.Entries[0].ActingAs)
	}
}

func TestTrashOfAnotherUserDoesNotCountAgainstMyQuota(t *testing.T) {
	e := newEnv(t)
	alice, bob := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	up := multipartUpload(t, "big.bin", bytes.Repeat([]byte("x"), 4096))
	if r := alice.do("POST", "/api/fs/upload?path=d", up.body, up.headers); r.status != http.StatusOK {
		t.Fatalf("上传失败：%d %s", r.status, r.body)
	}
	// alice 删除 → 回收站占用 4096
	alice.must("POST", "/api/fs/delete", map[string]any{"paths": []string{"d/big.bin"}}, http.StatusOK)
	// 给 bob 一个刚好够放下自己文件的配额
	bu, _ := e.store.User("bob")
	bu.Quota = 5000
	if err := e.store.PutUser(bu); err != nil {
		t.Fatal(err)
	}
	bob.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "b"}, http.StatusOK)
	up2 := multipartUpload(t, "mine.bin", bytes.Repeat([]byte("y"), 4000))
	if r := bob.do("POST", "/api/fs/upload?path=b", up2.body, up2.headers); r.status != http.StatusOK {
		t.Fatalf("bob 的上传不应受 alice 回收站影响，实际 %d %s", r.status, r.body)
	}
	// 普通账号能看"自己"的回收站（按账号隔离），且不会看到别人的条目
	own := bob.must("GET", "/api/trash", nil, http.StatusOK)
	if items, _ := own["items"].([]any); len(items) != 0 {
		t.Fatalf("bob 的回收站不应包含 alice 的条目：%v", own)
	}
	adm := e.newAPI()
	adm.login("admin", "admin")
	list := adm.must("GET", "/api/trash?user=bob", nil, http.StatusOK)
	if items, _ := list["items"].([]any); len(items) != 0 {
		t.Fatalf("bob 的回收站不应包含 alice 的条目：%v", list)
	}
}

// 回收站占用的是"同一个"配额：删除不改动 used+trash 之和，恢复也不应改动。
// （Restore 内部确实没有配额检查，但 CheckQuota 一直把 trash 计入，所以 API 上无法利用——
// 见报告中的 D9：属于"缺少防御性检查"，而不是可利用漏洞。）
func TestTrashAndRestoreKeepTotalUsageWithinQuota(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	au, _ := e.store.User("alice")
	au.Quota = 10000
	if err := e.store.PutUser(au); err != nil {
		t.Fatal(err)
	}
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	up := multipartUpload(t, "a.bin", bytes.Repeat([]byte("a"), 5000))
	if r := alice.do("POST", "/api/fs/upload?path=d", up.body, up.headers); r.status != http.StatusOK {
		t.Fatalf("上传失败：%d %s", r.status, r.body)
	}
	if r := alice.json("POST", "/api/fs/delete", map[string]any{"paths": []string{"d/a.bin"}}); r.status != http.StatusOK {
		t.Fatalf("删除失败：%d %s", r.status, r.body)
	}
	// 回收站里的 5000 依然算配额：再传 9999 必须被拒
	up2 := multipartUpload(t, "b.bin", bytes.Repeat([]byte("b"), 9999))
	if r := alice.do("POST", "/api/fs/upload?path=", up2.body, up2.headers); r.status != http.StatusInsufficientStorage {
		t.Fatalf("回收站占用配额，超限上传应 507，实际 %d %s", r.status, r.body)
	}
	// 恢复一条 → 重新回到"已用 5000"，此时还能再放 5000（回收站操作由管理员执行）
	adm := e.newAPI()
	adm.login("admin", "admin")
	trash := adm.must("GET", "/api/trash?user=alice", nil, http.StatusOK)
	items, _ := trash["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("回收站应有 1 条：%v", trash)
	}
	id := items[0].(map[string]any)["id"].(string)
	rr := adm.json("POST", "/api/trash/restore?user=alice", map[string]any{
		"ids": []string{id}, "password": "admin",
	})
	if rr.status != http.StatusOK {
		t.Fatalf("恢复失败：%d %s", rr.status, rr.body)
	}
	if items, _ := adm.must("GET", "/api/trash?user=alice", nil, http.StatusOK)["items"].([]any); len(items) != 0 {
		t.Fatalf("恢复后回收站应为空：%v", items)
	}
	up3 := multipartUpload(t, "c.bin", bytes.Repeat([]byte("c"), 5000))
	if r := alice.do("POST", "/api/fs/upload?path=", up3.body, up3.headers); r.status != http.StatusOK {
		t.Fatalf("恢复后应还能再放 5000：%d %s", r.status, r.body)
	}
}

// 删除只是把文件移进回收站，used+trash 不变，因此即使已经用满配额也必须允许删除。
func TestDeleteAtQuotaIsAllowed(t *testing.T) {
	regression(t, "#S9",
		"core.Trash（core/service.go:457）用 CheckQuota(u, 要删除的大小) 校验，而配额公式是 "+
			"used+trash+add>Quota（service.go:167-180）；删除既不加 used 也不加 trash，"+
			"于是\"把配额填满的文件删掉\"这件最需要做的事反而会 507 失败，用户被卡死")
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	au, _ := e.store.User("alice")
	au.Quota = 3000
	if err := e.store.PutUser(au); err != nil {
		t.Fatal(err)
	}
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	up := multipartUpload(t, "a.bin", bytes.Repeat([]byte("a"), 2900))
	if r := alice.do("POST", "/api/fs/upload?path=d", up.body, up.headers); r.status != http.StatusOK {
		t.Fatalf("上传失败：%d %s", r.status, r.body)
	}
	if r := alice.json("POST", "/api/fs/delete", map[string]any{"paths": []string{"d/a.bin"}}); r.status != http.StatusOK {
		t.Fatalf("已用满配额时删除自己刚上传的文件必须成功，实际 %d %s", r.status, r.body)
	}
}

func ipHdr(ip string) map[string]string {
	return map[string]string{"X-Forwarded-For": ip}
}

// 限速器同时按"用户名"和"IP"计数（auth.Limiter.Allowed 检查 byUser 与 byIP）。
func TestLoginLimiterLocksPerUserAndPerIP(t *testing.T) {
	e := newEnv(t)
	setupAliceBob(t, e)
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("PATCH", "/api/settings", map[string]any{"trustProxy": true, "adminPassword": "admin"}, http.StatusOK)

	att := e.newAPI()
	wrong := map[string]string{"username": "alice", "password": "wrong-password"}
	// 错误 3 次后被一次成功登录重置（用户名与 IP 的计数都清空）
	for i := 0; i < 3; i++ {
		if r := att.do("POST", "/api/auth/login", wrong, ipHdr("1.1.1.1")); r.status != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误登录应 401，实际 %d %s", i+1, r.status, r.body)
		}
	}
	if r := att.do("POST", "/api/auth/login",
		map[string]string{"username": "alice", "password": "alice12345"}, ipHdr("1.1.1.1")); r.status != http.StatusOK {
		t.Fatalf("成功登录应重置计数，实际 %d %s", r.status, r.body)
	}
	for i := 0; i < 3; i++ {
		if r := att.do("POST", "/api/auth/login", wrong, ipHdr("1.1.1.1")); r.status != http.StatusUnauthorized {
			t.Fatalf("重置后第 %d 次失败应 401，实际 %d", i+1, r.status)
		}
	}
	for i := 0; i < 2; i++ {
		att.do("POST", "/api/auth/login", wrong, ipHdr("1.1.1.1"))
	}
	r := att.do("POST", "/api/auth/login", wrong, ipHdr("1.1.1.1"))
	if r.status != http.StatusTooManyRequests || e.code(r) != "too_many_attempts" {
		t.Fatalf("累计 5 次失败后应 429，实际 %d %s", r.status, r.body)
	}
	// 429 必须带 Retry-After（正整数秒），客户端才能正确退避
	if ra := r.header.Get("Retry-After"); ra == "" {
		t.Fatal("429 响应必须带 Retry-After 头")
	} else if sec, err := strconv.Atoi(ra); err != nil || sec < 1 {
		t.Fatalf("Retry-After 应是正整数秒，实际 %q", ra)
	}
	// 账号维度：换 IP 也照样被锁
	r = att.do("POST", "/api/auth/login",
		map[string]string{"username": "alice", "password": "alice12345"}, ipHdr("2.2.2.2"))
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("账号维度锁定后，换 IP 用正确密码也应 429，实际 %d %s", r.status, r.body)
	}
	// IP 维度不再连带锁定：alice 一个人失败 5 次，同 IP 上的 bob 仍可正常登录
	r = att.do("POST", "/api/auth/login",
		map[string]string{"username": "bob", "password": "bob1234567"}, ipHdr("1.1.1.1"))
	if r.status != http.StatusOK {
		t.Fatalf("同一 IP 上的其它账号不应被连带锁定，实际 %d %s", r.status, r.body)
	}
	// 换个 IP 当然也正常
	r = att.do("POST", "/api/auth/login",
		map[string]string{"username": "bob", "password": "bob1234567"}, ipHdr("3.3.3.3"))
	if r.status != http.StatusOK {
		t.Fatalf("不同 IP 的其它账号应正常登录，实际 %d %s", r.status, r.body)
	}
}

// 同一 IP 上大量**不同**账号失败（撞库特征）时才封 IP，避免 NAT 连坐。
func TestLoginLimiterLocksIPOnlyOnCredentialStuffing(t *testing.T) {
	e := newEnv(t)
	setupAliceBob(t, e)
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("PATCH", "/api/settings", map[string]any{"trustProxy": true, "adminPassword": "admin"}, http.StatusOK)

	att := e.newAPI()
	// 8 个不同账号各失败一次 → 该 IP 命中撞库特征被封
	for i := 0; i < 8; i++ {
		att.do("POST", "/api/auth/login",
			map[string]string{"username": fmt.Sprintf("ghost%d", i), "password": "nope"}, ipHdr("7.7.7.7"))
	}
	r := att.do("POST", "/api/auth/login",
		map[string]string{"username": "alice", "password": "alice12345"}, ipHdr("7.7.7.7"))
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("同 IP 上 8 个不同账号失败后应封 IP，实际 %d %s", r.status, r.body)
	}
	// 换 IP 正常
	r = att.do("POST", "/api/auth/login",
		map[string]string{"username": "alice", "password": "alice12345"}, ipHdr("8.8.8.8"))
	if r.status != http.StatusOK {
		t.Fatalf("换 IP 应正常登录，实际 %d %s", r.status, r.body)
	}
}

// 只要攻击者与受害者共用一个出口 IP（办公网 NAT、反向代理未配好 trustProxy），
// 攻击者的 5 次失败就会把整个公司锁在门外 5 分钟。
func TestLoginLimiterDoesNotLockOtherAccountsFromSameIP(t *testing.T) {
	regression(t, "#S10",
		"auth.Limiter.Fail 同时给 byUser 和 byIP 计数（auth.go:242-257），Allowed 任一命中即拒绝"+
			"（auth.go:232-238）。攻击者用任意错误密码打 5 次，就能把该出口 IP 上的**所有**账号"+
			"锁 5 分钟（NAT/共享代理下影响面是整个办公网）；且 429 不带 Retry-After 头")
	e := newEnv(t)
	setupAliceBob(t, e)
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("PATCH", "/api/settings", map[string]any{"trustProxy": true, "adminPassword": "admin"}, http.StatusOK)

	att := e.newAPI()
	for i := 0; i < 5; i++ {
		att.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "nope"}, ipHdr("9.9.9.9"))
	}
	r := att.do("POST", "/api/auth/login",
		map[string]string{"username": "bob", "password": "bob1234567"}, ipHdr("9.9.9.9"))
	if r.status != http.StatusOK {
		t.Fatalf("同一 IP 上的其它账号不应被连带锁定，实际 %d %s", r.status, r.body)
	}
}

// 修改凭据接口对"当前密码"的校验完全没有接入限速器，可无限爆破。
func TestChangeCredentialsBruteForceIsNotLimited(t *testing.T) {
	regression(t, "#S6",
		"handleChangeCredentials（handlers_auth.go:148）直接用 VerifyPassword 校验当前密码，"+
			"既不检查 limiter.Allowed 也不在失败时 limiter.Fail，因此可以对已登录会话无限猜当前密码"+
			"（登录接口 5 次就锁，这里永远不锁）")
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	var last resp
	for i := 0; i < 12; i++ {
		last = alice.json("POST", "/api/auth/change-credentials", map[string]any{
			"currentPassword": "guess-" + strings.Repeat("x", i+1), "newPassword": "whatever12345"})
	}
	if last.status != http.StatusTooManyRequests {
		t.Fatalf("连续 12 次错误的当前密码后应被限速（429），实际 %d %s", last.status, last.body)
	}
}

func TestAdminCanSeeAllUploadSessions(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	sess := alice.must("POST", "/api/upload/init",
		map[string]any{"dir": "d", "name": "big.bin", "size": 1024, "chunkSize": 1024}, http.StatusOK)

	admin := e.newAPI()
	admin.login("admin", "admin")
	list := admin.must("GET", "/api/upload", nil, http.StatusOK)
	sessions, _ := list["sessions"].([]any)
	found := false
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["id"] == sess["id"] {
			found = true
			if m["owner"] != "alice" {
				t.Fatalf("管理员列表应带 owner，实际 %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("管理员应能看到 alice 的会话：%v", list)
	}
	// 普通用户只看自己的
	bl := e.newAPI()
	bl.login("bob", "bob1234567")
	list = bl.must("GET", "/api/upload", nil, http.StatusOK)
	if sessions, _ := list["sessions"].([]any); len(sessions) != 0 {
		t.Fatalf("bob 不应看到 alice 的会话：%v", list)
	}
}

// 管理员不带 ?user= 时没有自己的存储空间，因此也不能碰别人的上传会话。
func TestAdminCannotTouchOthersUploadSessionWithoutReauth(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	alice.must("POST", "/api/fs/mkdir", map[string]any{"dir": "", "name": "d"}, http.StatusOK)
	sess := alice.must("POST", "/api/upload/init",
		map[string]any{"dir": "d", "name": "big.bin", "size": 2048, "chunkSize": 1024}, http.StatusOK)
	id := sess["id"].(string)

	admin := e.newAPI()
	admin.login("admin", "admin")
	rejected := func(r resp) bool {
		return r.status == http.StatusPreconditionRequired || r.status == http.StatusForbidden
	}
	// 写分片：必须被拒绝（428 需要密码，或 403 因为管理员没有目标存储）
	r := admin.do("PUT", "/api/upload/"+id+"/0", bytes.Repeat([]byte("A"), 2048), nil)
	if !rejected(r) {
		t.Fatalf("管理员无目标用户写他人会话分片必须被拒绝，实际 %d %s", r.status, r.body)
	}
	// 取消会话：同样必须被拒绝
	r = admin.json("DELETE", "/api/upload/"+id, nil)
	if !rejected(r) {
		t.Fatalf("管理员无目标用户取消他人会话必须被拒绝，实际 %d %s", r.status, r.body)
	}
	// 关键：alice 的会话既没被写入分片，也没被取消，她还能正常续传完成
	st := alice.must("GET", "/api/upload/"+id, nil, http.StatusOK)
	if received, _ := st["received"].([]any); len(received) != 0 {
		t.Fatalf("管理员被拒绝后不应写入任何分片，实际 %v", st["received"])
	}
	alice.do("PUT", "/api/upload/"+id+"/0", bytes.Repeat([]byte("A"), 2048), nil)
	if r := alice.json("POST", "/api/upload/"+id+"/complete", nil); r.status != http.StatusOK {
		t.Fatalf("alice 应能正常完成自己的上传，实际 %d %s", r.status, r.body)
	}
}

// 提权路径已被结构性封死：
//  1. 普通账号不能拥有 users_manage（后端直接拒绝这种授权）；
//  2. 即使拿到了，/api/users 也只允许管理员账号访问；
//  3. 创建接口不再接受 isAdmin（系统只有一个管理员）。
func TestNonAdminCannotEscalateToAdmin(t *testing.T) {
	e := newEnv(t)
	admin := e.newAPI()
	admin.login("admin", "admin")

	// 1) 想把 users_manage 授给普通账号 → 400
	r := admin.json("POST", "/api/users", map[string]any{
		"username": "ops", "password": "ops12345678", "root": "ops",
		"permissions": []string{"users_manage"}, "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest || e.code(r) != "admin_only_permission" {
		t.Fatalf("普通账号不应被授予 users_manage，实际 %d %s", r.status, r.body)
	}

	// 2) 普通账号（只有 read/upload）访问用户管理 → 403
	admin.must("POST", "/api/users", map[string]any{
		"username": "ops", "password": "ops12345678", "root": "ops",
		"permissions": []string{"read", "upload"}, "adminPassword": "admin",
	}, http.StatusOK)
	ops := e.newAPI()
	ops.login("ops", "ops12345678")
	if r := ops.json("GET", "/api/users", nil); r.status != http.StatusForbidden {
		t.Fatalf("普通账号读取用户列表应 403，实际 %d %s", r.status, r.body)
	}
	if r := ops.json("POST", "/api/users", map[string]any{
		"username": "evil", "password": "evil12345678", "isAdmin": true,
	}); r.status != http.StatusForbidden {
		t.Fatalf("普通账号创建账号应 403，实际 %d %s", r.status, r.body)
	}

	// 3) 管理员也不能创建第二个管理员
	r = admin.json("POST", "/api/users", map[string]any{
		"username": "admin2", "password": "admin2pass", "isAdmin": true, "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest || e.code(r) != "admin_unique" {
		t.Fatalf("系统只允许一个管理员，实际 %d %s", r.status, r.body)
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	e := newEnv(t)
	alice, _ := setupAliceBob(t, e)
	au, _ := e.store.User("alice")
	root, err := e.svc.FS.EnsureRoot(au.Root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(e.cfg.SpaceDir, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	const marker = "OUTSIDE-MARKER"
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(marker), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前环境无法创建符号链接（%v），跳过", err)
	}
	// 列出时不应跟随
	list := alice.must("GET", "/api/fs/list?path=", nil, http.StatusOK)
	for _, en := range list["entries"].([]any) {
		if en.(map[string]any)["name"] == "link" {
			t.Fatal("列表不应返回符号链接条目")
		}
	}
	// 通过链接读取文件必须失败
	for _, p := range []string{"link/secret.txt", "link"} {
		r := alice.json("GET", "/api/fs/download?path="+url.QueryEscape(p), nil)
		if r.status == http.StatusOK || strings.Contains(string(r.body), marker) {
			t.Fatalf("通过符号链接 %q 读取到了外部文件：%d %s", p, r.status, r.body)
		}
	}
}

// 两个用户被配置成"父子"根目录（shared 与 shared/team）时必须被拒绝，
// 否则两个账号共享同一份物理目录，可以互相读写、互相覆盖。
func TestOverlappingRootsAreRejected(t *testing.T) {
	e := newEnv(t)
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("POST", "/api/users", map[string]any{
		"username": "owner", "password": "owner12345678", "root": "shared",
		"adminPassword": "admin",
	}, http.StatusOK)

	// 子目录形式：shared/team 落在 owner 的根里
	r := admin.json("POST", "/api/users", map[string]any{
		"username": "guest", "password": "guest12345678", "root": "shared/team",
		"adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest || e.code(r) != "root_conflict" {
		t.Fatalf("父子根目录必须被拒绝（400 root_conflict），实际 %d %s", r.status, r.body)
	}
	// 反向：先有 shared/team，再建 shared 也要拒绝
	admin.must("POST", "/api/users", map[string]any{
		"username": "guest", "password": "guest12345678", "root": "shared/team",
		"adminPassword": "admin",
	}, http.StatusBadRequest)
	// 另一个账号把自己的根改到 owner 的子树里同样要拒绝
	admin.must("POST", "/api/users", map[string]any{
		"username": "other", "password": "other12345678", "root": "other",
		"adminPassword": "admin",
	}, http.StatusOK)
	r = admin.json("PATCH", "/api/users/other", map[string]any{
		"root": "shared/team", "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest || e.code(r) != "root_conflict" {
		t.Fatalf("把根目录改到别人的子树里必须被拒绝，实际 %d %s", r.status, r.body)
	}
	// owner 把自己的根收窄到自己的子树里是合法的（还是自己的空间）
	admin.must("PATCH", "/api/users/owner", map[string]any{
		"root": "shared/inner", "adminPassword": "admin",
	}, http.StatusOK)
	// 换了不冲突的根目录就能建
	admin.must("POST", "/api/users", map[string]any{
		"username": "guest", "password": "guest12345678", "root": "guest",
		"adminPassword": "admin",
	}, http.StatusOK)
}

// Windows 下文件名大小写不敏感：root=sharedcase 与 root=SHAREDCASE 是同一个目录，必须拒绝。
func TestCaseFoldedRootsAreRejected(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("大小写折叠只在 Windows/macOS 上出现")
	}
	e := newEnv(t)
	admin := e.newAPI()
	admin.login("admin", "admin")
	admin.must("POST", "/api/users", map[string]any{
		"username": "lower", "password": "lower12345678", "root": "sharedcase",
		"adminPassword": "admin",
	}, http.StatusOK)
	r := admin.json("POST", "/api/users", map[string]any{
		"username": "upper", "password": "upper12345678", "root": "SHAREDCASE",
		"adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest || e.code(r) != "root_conflict" {
		t.Fatalf("只差大小写的根目录必须被拒绝，实际 %d %s", r.status, r.body)
	}
}

// 用户配额之和不能超过空间总容量：10 GB 的空间里 A 拿了 8 GB，B 最多只能再拿 2 GB。
func TestUserQuotaBudget(t *testing.T) {
	e := newEnv(t)
	admin := e.newAPI()
	admin.login("admin", "admin")
	// 测试环境把空间放大到 1 TiB，这里显式收紧到 10 GiB
	admin.must("PATCH", "/api/settings", map[string]any{
		"spaceQuota": 10 << 30, "adminPassword": "admin",
	}, http.StatusOK)

	admin.must("POST", "/api/users", map[string]any{
		"username": "alice", "password": "alice12345", "root": "alice",
		"quota": 8 << 30, "adminPassword": "admin",
	}, http.StatusOK)

	// B 想拿 3 GiB → 超预算
	r := admin.json("POST", "/api/users", map[string]any{
		"username": "bob", "password": "bob1234567", "root": "bob",
		"quota": 3 << 30, "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest || e.code(r) != "quota_budget" {
		t.Fatalf("超过空间总容量应 400 quota_budget，实际 %d %s", r.status, r.body)
	}
	// 2 GiB 正好放得下
	admin.must("POST", "/api/users", map[string]any{
		"username": "bob", "password": "bob1234567", "root": "bob",
		"quota": 2 << 30, "adminPassword": "admin",
	}, http.StatusOK)
	// 再把空间容量降到 5 GiB（低于已分配 10 GiB）→ 拒绝
	r = admin.json("PATCH", "/api/settings", map[string]any{
		"spaceQuota": 5 << 30, "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest {
		t.Fatalf("空间容量低于已分配配额之和应被拒绝，实际 %d %s", r.status, r.body)
	}
	// 用户自己也不能被改成超过剩余预算
	r = admin.json("PATCH", "/api/users/bob", map[string]any{
		"quota": 3 << 30, "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest || e.code(r) != "quota_budget" {
		t.Fatalf("调高单账号配额也不能超预算，实际 %d %s", r.status, r.body)
	}
}
