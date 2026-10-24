package server_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 管理页面改监听端口：必须落到 config.ini，并且明确告诉前端"要重启"。
func TestSettingsListenPersisted(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")

	got := e.mustJSON("GET", "/api/settings", nil, http.StatusOK)
	eff, _ := got["effective"].(map[string]any)
	orig, _ := eff["listen"].(string)
	if orig == "" {
		t.Fatalf("effective.listen 不应为空：%v", got)
	}

	saved := e.mustJSON("PATCH", "/api/settings", map[string]any{
		"listen": "0.0.0.0:19090", "adminPassword": "admin",
	}, http.StatusOK)
	if !containsAny(saved["restartRequired"], "listen") {
		t.Fatalf("修改监听地址应要求重启：%v", saved)
	}

	raw, err := os.ReadFile(e.cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `listen = "0.0.0.0:19090"`) {
		t.Fatalf("config.ini 没被改写：\n%s", raw)
	}
	// 其它键必须保留（只改一行，不能把文件重写丢内容）。
	if !strings.Contains(string(raw), "space_dir") {
		t.Fatalf("config.ini 里的 space_dir 被写丢了：\n%s", raw)
	}

	// 再读一次：待生效状态要能被前端看到。
	again := e.mustJSON("GET", "/api/settings", nil, http.StatusOK)
	if !containsAny(again["restartRequired"], "listen") {
		t.Fatalf("重启提示应持续到真正重启为止：%v", again)
	}
	settings, _ := again["settings"].(map[string]any)
	if settings["listen"] != "0.0.0.0:19090" {
		t.Fatalf("待生效的 listen 没回显：%v", settings)
	}

	// 改回原值 = 撤销待生效项，不再提示重启。
	back := e.mustJSON("PATCH", "/api/settings", map[string]any{
		"listen": orig, "adminPassword": "admin",
	}, http.StatusOK)
	if containsAny(back["restartRequired"], "listen") {
		t.Fatalf("改回原值后不应再要求重启：%v", back)
	}
}

func TestSettingsListenRejectsGarbage(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")
	for _, bad := range []string{"", "abc", "0.0.0.0", "0.0.0.0:99999", "0.0.0.0:-1"} {
		r := e.json("PATCH", "/api/settings", map[string]any{
			"listen": bad, "adminPassword": "admin",
		})
		if r.status == http.StatusOK {
			t.Fatalf("非法监听地址 %q 应被拒绝，实际 200", bad)
		}
	}
}

// TLS 两项必须成对出现，且文件必须真的可读——否则重启后进程起不来。
func TestSettingsTLSRequiresPair(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")

	r := e.json("PATCH", "/api/settings", map[string]any{
		"tlsCert": filepath.Join(t.TempDir(), "a.pem"), "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest {
		t.Fatalf("只给证书应返回 400，实际 %d %s", r.status, r.body)
	}

	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 私钥文件不存在 → 拒绝
	r = e.json("PATCH", "/api/settings", map[string]any{
		"tlsCert": cert, "tlsKey": key, "adminPassword": "admin",
	})
	if r.status != http.StatusBadRequest {
		t.Fatalf("私钥文件不存在应返回 400，实际 %d %s", r.status, r.body)
	}
}

// 目录浏览器：只列目录、只认绝对路径、普通账号无权访问。
func TestBrowseDirs(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")

	// 空间目录里本来就有 files/ trash/ audit/ uploads/ cache/。
	got := e.mustJSON("GET", "/api/system/browse?path="+url.QueryEscape(e.cfg.SpaceDir), nil, http.StatusOK)
	dirs, ok := got["dirs"].([]any)
	if !ok || len(dirs) == 0 {
		t.Fatalf("空间目录下应能列出子目录：%v", got)
	}
	for _, d := range dirs {
		m := d.(map[string]any)
		name, _ := m["name"].(string)
		abs, _ := m["path"].(string)
		if name == "" || abs == "" {
			t.Fatalf("目录项字段不完整：%v", m)
		}
		if !filepath.IsAbs(abs) {
			t.Fatalf("返回的路径应为绝对路径：%v", m)
		}
	}
	// 回显的是清理过的绝对路径（Windows 上会统一成反斜杠）。
	if filepath.Clean(got["path"].(string)) != filepath.Clean(e.cfg.SpaceDir) {
		t.Fatalf("path 回显不正确：%v != %v", got["path"], e.cfg.SpaceDir)
	}

	// 相对路径一律拒绝。
	if r := e.json("GET", "/api/system/browse?path=relative/dir", nil); r.status != http.StatusBadRequest {
		t.Fatalf("相对路径应返回 400，实际 %d %s", r.status, r.body)
	}
	// 不存在的目录给 400 而不是 500，方便前端提示。
	if r := e.json("GET", "/api/system/browse?path=/nonexistent-hv-dir-xyz", nil); r.status != http.StatusBadRequest {
		t.Fatalf("不存在的目录应返回 400，实际 %d %s", r.status, r.body)
	}
}

func TestBrowseDirsDeniedForNormalUser(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")
	e.mustJSON("POST", "/api/users", map[string]any{
		"username": "carol", "password": "carol12345", "root": "carol",
		"adminPassword": "admin",
	}, http.StatusOK)
	e.logout()
	e.loginAs("carol", "carol12345")

	if r := e.json("GET", "/api/system/browse?path=/", nil); r.status == http.StatusOK {
		t.Fatalf("普通账号不应能浏览本机目录：%d %s", r.status, r.body)
	}
}

// 建用户时存储目录创建失败必须回滚，不能留下"界面报错但账号已存在"的幽灵记录。
func TestUserCreateRollsBackWhenRootFails(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")

	// 在 files/ 下放一个同名**文件**，让 MkdirAll(files/bob) 必然失败。
	if err := os.WriteFile(filepath.Join(e.cfg.FilesPath(), "bob"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.json("POST", "/api/users", map[string]any{
		"username": "bob", "password": "bob12345678", "root": "bob",
		"adminPassword": "admin",
	})
	if r.status == http.StatusOK {
		t.Fatalf("存储目录创建失败时不应返回成功：%d %s", r.status, r.body)
	}
	users := e.mustJSON("GET", "/api/users", nil, http.StatusOK)
	list, _ := users["users"].([]any)
	for _, u := range list {
		if m, ok := u.(map[string]any); ok && m["username"] == "bob" {
			t.Fatalf("建目录失败后账号应被回滚，实际仍存在：%v", m)
		}
	}
}

func containsAny(v any, want string) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}
