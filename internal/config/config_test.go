package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"homevault/internal/config"
)

// regression：这些用例原本用于复现已知缺陷；缺陷修复后改为默认运行的回归用例，id/desc 只用于回溯。
func regression(t *testing.T, id, desc string) {
	t.Helper()
	// 该缺陷已修复，用例默认运行；id/desc 仅用于回溯历史。
	t.Logf("回归用例 %s：%s", id, desc)
}

func writeFile(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.ini")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	return p
}

func load(t *testing.T, text string) *config.Config {
	t.Helper()
	cfg, err := config.Load(writeFile(t, text))
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	return cfg
}

func loadErr(t *testing.T, text string) error {
	t.Helper()
	_, err := config.Load(writeFile(t, text))
	if err == nil {
		t.Fatalf("期望 Load 报错，实际成功（配置：%q）", text)
	}
	return err
}

// 配置文件不存在时必须写出模板并返回一套可用的默认值。
func TestLoadMissingFileWritesTemplateAndUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.ini")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("首次 Load 失败：%v", err)
	}
	if cfg.Listen != config.DefaultListen {
		t.Fatalf("默认 listen 不正确：%q", cfg.Listen)
	}
	if cfg.AdminUser != "admin" || cfg.AdminPassword != "admin" {
		t.Fatalf("默认管理员不正确：%q/%q", cfg.AdminUser, cfg.AdminPassword)
	}
	if cfg.SpaceDir != config.DefaultSpaceDir() {
		t.Fatalf("模板里 space_dir 为空时应退回默认目录，实际 %q", cfg.SpaceDir)
	}
	if cfg.Path != path {
		t.Fatalf("Path 应为绝对路径 %q，实际 %q", path, cfg.Path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("模板文件没有被写出：%v", err)
	}
	if string(raw) != config.Template {
		t.Fatalf("写出的模板与 config.Template 不一致")
	}
	// 第二次 Load 必须能解析自己写出的模板
	if _, err := config.Load(path); err != nil {
		t.Fatalf("第二次 Load 模板失败：%v", err)
	}
	// 默认值本身必须能通过校验
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("Default() 未通过 Validate：%v", err)
	}
}

func TestParseQuotesCommentsAndSpaces(t *testing.T) {
	dir := t.TempDir()
	space := filepath.Join(dir, "space with space")
	cert := filepath.Join(dir, "cert.pem")

	cfg := load(t, strings.Join([]string{
		"# 注释行",
		"; 也是注释行",
		"",
		"  listen\t=\t\"127.0.0.1:9000\"  ",
		`space_dir = "` + space + `"`,
		"tls_cert = " + cert, // 无引号绝对路径
		"tls_key = key.pem",  // 无引号相对路径
		"admin_user = bob # 行尾注释",
		"admin_password = p#ss",  // 无引号：'#' 之后是注释
		"ADMIN_PASSWORD = final", // 大小写不敏感 + 后出现者覆盖
	}, "\n")+"\n")

	if cfg.Listen != "127.0.0.1:9000" {
		t.Fatalf("带引号/空格的 listen 解析错误：%q", cfg.Listen)
	}
	if cfg.SpaceDir != space {
		t.Fatalf("space_dir 解析错误：%q（期望 %q）", cfg.SpaceDir, space)
	}
	if cfg.TLSCert != cert {
		t.Fatalf("无引号绝对路径解析错误：%q", cfg.TLSCert)
	}
	if cfg.AdminUser != "bob" {
		t.Fatalf("行尾 # 注释没有被去掉：%q", cfg.AdminUser)
	}
	if cfg.AdminPassword != "final" {
		t.Fatalf("大小写不敏感/覆盖语义错误：%q", cfg.AdminPassword)
	}
	if !filepath.IsAbs(cfg.TLSKey) {
		t.Fatalf("相对 tls_key 应转为绝对路径：%q", cfg.TLSKey)
	}
	if cfg.TLSKey != filepath.Join(filepath.Dir(cfg.Path), "key.pem") {
		t.Fatalf("相对路径应相对配置文件目录：%q", cfg.TLSKey)
	}
	if !cfg.TLSEnabled() {
		t.Fatalf("证书与私钥都非空时应启用 TLS")
	}
}

// 引号内的 '#' / ';' / 空格不能被当注释截断。
func TestQuotedValueKeepsHashAndSemicolon(t *testing.T) {
	cfg := load(t, "listen = \"0.0.0.0:8080#frag\"\nadmin_user = \"a;b\"\nadmin_password = \" p w \"\n")
	if cfg.Listen != "0.0.0.0:8080#frag" {
		t.Fatalf("引号内的 # 被截断：%q", cfg.Listen)
	}
	if cfg.AdminUser != "a;b" {
		t.Fatalf("引号内的 ; 被截断：%q", cfg.AdminUser)
	}
	if cfg.AdminPassword != " p w " {
		t.Fatalf("引号内的前后空格应保留：%q", cfg.AdminPassword)
	}
}

// 引号值后面允许跟 "#"/";" 行尾注释（D14 修复）；引号后出现其它内容仍报错。
func TestQuotedValueWithTrailingComment(t *testing.T) {
	// 这里刻意不用 space_dir：它会被 finalize 成绝对路径，跨平台断言会引入噪音。
	cfg := load(t, "listen = \"0.0.0.0:1\" # 监听地址\nadmin_password = \"pw\" ; 管理员密码\n")
	if cfg.Listen != "0.0.0.0:1" {
		t.Fatalf("引号值后的 # 行尾注释应被忽略：%q", cfg.Listen)
	}
	if cfg.AdminPassword != "pw" {
		t.Fatalf("引号值后的分号注释同样应被忽略：%q", cfg.AdminPassword)
	}
	// 引号闭合后除了注释不该有别的字符
	if err := loadErr(t, "listen = \"0.0.0.0:1\" garbage\n"); err == nil {
		t.Fatal("引号值后的非注释内容必须报错")
	}
}

func TestParseCRLF(t *testing.T) {
	cfg := load(t, "listen = \"127.0.0.1:1\"\r\n# 注释\r\nadmin_user = \"carol\"\r\nadmin_password = \"pw\"\r\n")
	if cfg.Listen != "127.0.0.1:1" {
		t.Fatalf("CRLF 下 listen 解析错误：%q", cfg.Listen)
	}
	if cfg.AdminUser != "carol" || cfg.AdminPassword != "pw" {
		t.Fatalf("CRLF 下管理员解析错误：%q/%q", cfg.AdminUser, cfg.AdminPassword)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"未知键必须报错", "listen = \"x:1\"\nquota = 10GiB\n", "未知配置项"},
		{"未知键（拼错）", "lstn = \"x:1\"\n", "未知配置项"},
		{"不支持分节", "[server]\nlisten = \"x:1\"\n", "不支持分节"},
		{"缺少等号", "listen\n", "缺少 '='"},
		{"listen 为空（引号）", "listen = \"\"\n", "listen 不能为空"},
		{"listen 为空（无引号）", "listen =   \n", "listen 不能为空"},
		{"admin_user 为空", "admin_user = \"\"\n", "admin_user 不能为空"},
		{"引号未闭合", "listen = \"abc\n", "引号未闭合"},
		{"引号未闭合（只有一个引号）", "listen = \"\n", "引号未闭合"},
		{"val 里的等号保留", "admin_password = a=b\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.want == "" {
				cfg := load(t, c.text)
				if cfg.AdminPassword != "a=b" {
					t.Fatalf("值里允许出现 '='，实际 %q", cfg.AdminPassword)
				}
				return
			}
			err := loadErr(t, c.text)
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", c.want, err)
			}
		})
	}
}

// space_dir 留空 → 系统默认；相对路径 → 相对配置文件目录。
func TestSpaceDirDefaultAndRelative(t *testing.T) {
	cfg := load(t, "listen = \"0.0.0.0:1\"\nspace_dir = \"\"\n")
	if cfg.SpaceDir != config.DefaultSpaceDir() {
		t.Fatalf("空 space_dir 应退回默认值，实际 %q", cfg.SpaceDir)
	}
	// 完全没写 space_dir 时同样退回默认值
	cfg2 := load(t, "listen = \"0.0.0.0:1\"\n")
	if cfg2.SpaceDir != config.DefaultSpaceDir() {
		t.Fatalf("缺省 space_dir 应退回默认值，实际 %q", cfg2.SpaceDir)
	}
	// 缺省 admin_user / admin_password 时回落到 admin/admin
	if cfg2.AdminUser != "admin" || cfg2.AdminPassword != "admin" {
		t.Fatalf("缺省管理员应回落 admin/admin，实际 %q/%q", cfg2.AdminUser, cfg2.AdminPassword)
	}

	p := writeFile(t, "listen = \"0.0.0.0:1\"\nspace_dir = mydata\n")
	cfg3, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(p), "mydata")
	if cfg3.SpaceDir != want {
		t.Fatalf("相对 space_dir 应解析为 %q，实际 %q", want, cfg3.SpaceDir)
	}
}

func TestValidateRules(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{Listen: "0.0.0.0:8080", SpaceDir: filepath.Join(t.TempDir(), "space")}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("基准配置应通过：%v", err)
	}
	c := base()
	c.Listen = "   "
	if err := c.Validate(); err == nil {
		t.Fatal("空 listen 必须报错")
	}
	c = base()
	c.SpaceDir = "relative/dir"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("非绝对 space_dir 必须报错，实际 %v", err)
	}
	c = base()
	c.SpaceDir = ""
	if err := c.Validate(); err == nil {
		t.Fatal("空 space_dir 必须报错")
	}
	c = base()
	c.TLSCert = filepath.Join(t.TempDir(), "cert.pem")
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "同时") {
		t.Fatalf("只设证书必须报错，实际 %v", err)
	}
	c = base()
	c.TLSKey = filepath.Join(t.TempDir(), "key.pem")
	if err := c.Validate(); err == nil {
		t.Fatal("只设私钥必须报错")
	}
}

func TestTLSEnabledNeedsBoth(t *testing.T) {
	cases := []struct {
		cert, key string
		want      bool
	}{
		{"", "", false},
		{"cert", "", false},
		{"", "key", false},
		{"cert", "key", true},
	}
	for _, c := range cases {
		cfg := &config.Config{TLSCert: c.cert, TLSKey: c.key}
		if got := cfg.TLSEnabled(); got != c.want {
			t.Fatalf("TLSEnabled(cert=%q,key=%q)=%v，期望 %v", c.cert, c.key, got, c.want)
		}
	}
}

func TestDefaultSpaceDirByGOOS(t *testing.T) {
	got := config.DefaultSpaceDir()
	switch runtime.GOOS {
	case "windows":
		if got != `C:\HomeVaultSpace` {
			t.Fatalf("Windows 默认空间目录不正确：%q", got)
		}
	default:
		if got != "/root/HomeVaultSpace" {
			t.Fatalf("非 Windows 默认空间目录不正确：%q", got)
		}
	}
	if config.Default().SpaceDir != got {
		t.Fatalf("Default().SpaceDir 应与 DefaultSpaceDir() 一致")
	}
}

// SetValue 只能改一行，且保留其余内容与注释。
func TestSetValuePreservesEverythingElse(t *testing.T) {
	p := writeFile(t, "# 头注释\nlisten = \"0.0.0.0:8080\"\n\nspace_dir = \"\"\n; 尾注释\n")
	if err := config.SetValue(p, "space_dir", "/data/space"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	want := "# 头注释\nlisten = \"0.0.0.0:8080\"\n\nspace_dir = \"/data/space\"\n; 尾注释\n"
	if string(raw) != want {
		t.Fatalf("SetValue 改动了多余内容：\n得到 %q\n期望 %q", raw, want)
	}

	// 大小写不敏感匹配 key，但重写时保留文件里原有的大小写（D15 修复）
	if err := config.SetValue(p, "LISTEN", "127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if !strings.Contains(string(raw), "listen = \"127.0.0.1:9\"") {
		t.Fatalf("重写时应保留文件里原有的大小写（listen）：%q", raw)
	}
	if n := strings.Count(strings.ToLower(string(raw)), "listen ="); n != 1 {
		t.Fatalf("listen 行数应仍为 1，实际 %d：%q", n, raw)
	}
	if strings.Count(string(raw), "space_dir") != 1 {
		t.Fatalf("space_dir 行数应仍为 1：%q", raw)
	}

	// key 不存在时追加
	if err := config.SetValue(p, "tls_cert", "/c.pem"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if !strings.Contains(string(raw), "tls_cert = \"/c.pem\"") {
		t.Fatalf("追加新键失败：%q", raw)
	}

	// 注释行里的 "key =" 不能被误改
	p2 := writeFile(t, "# space_dir = \"should-not-change\"\nspace_dir = \"\"\n")
	if err := config.SetValue(p2, "space_dir", "/x"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p2)
	if !strings.Contains(string(raw), "# space_dir = \"should-not-change\"") {
		t.Fatalf("注释行被改写：%q", raw)
	}
	if !strings.Contains(string(raw), "space_dir = \"/x\"") {
		t.Fatalf("真实行没有被改写：%q", raw)
	}
}

// SetValue 大小写不敏感地匹配 key，但重写目标行时保留文件里原有的大小写（D15 修复）。
func TestSetValuePreservesOriginalKeyCasing(t *testing.T) {
	p := writeFile(t, "listen = \"a\"\n")
	if err := config.SetValue(p, "LISTEN", "b"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "listen = ") {
		t.Fatalf("原文件里 key 的大小写应被保留，实际 %q", raw)
	}
	if !strings.Contains(string(raw), "listen = \"b\"") {
		t.Fatalf("值应被改写，实际 %q", raw)
	}
}

func TestSetValueCRLFKeepsOtherLines(t *testing.T) {
	p := writeFile(t, "listen = \"a\"\r\nspace_dir = \"\"\r\nadmin_user = \"admin\"\r\n")
	if err := config.SetValue(p, "space_dir", "/d"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "admin_user = \"admin\"\r\n") {
		t.Fatalf("其它 CRLF 行被破坏：%q", raw)
	}
	if !strings.Contains(string(raw), "space_dir = \"/d\"") {
		t.Fatalf("目标行没有被替换：%q", raw)
	}
}

func TestLayoutPathsAndEnsureLayout(t *testing.T) {
	space := filepath.Join(t.TempDir(), "HomeVaultSpace")
	cfg := &config.Config{Listen: "0.0.0.0:8080", SpaceDir: space}
	pairs := []struct{ got, want string }{
		{cfg.FilesPath(), filepath.Join(space, "files")},
		{cfg.TrashPath(), filepath.Join(space, "trash")},
		{cfg.UploadPath(), filepath.Join(space, "uploads")},
		{cfg.AuditPath(), filepath.Join(space, "audit")},
		{cfg.CachePath(), filepath.Join(space, "cache")},
		{cfg.StatePath(), filepath.Join(space, "state.json")},
	}
	for _, c := range pairs {
		if c.got != c.want {
			t.Fatalf("路径不正确：%q != %q", c.got, c.want)
		}
	}
	if cfg.HasState() {
		t.Fatal("尚未写 state.json，HasState 应为 false")
	}
	if err := cfg.EnsureLayout(); err != nil {
		t.Fatalf("EnsureLayout 失败：%v", err)
	}
	for _, d := range []string{space, cfg.FilesPath(), cfg.TrashPath(), cfg.UploadPath(), cfg.AuditPath(), cfg.CachePath()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("目录 %s 未创建：%v", d, err)
		}
	}
	if err := os.WriteFile(cfg.StatePath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !cfg.HasState() {
		t.Fatal("写入 state.json 后 HasState 应为 true")
	}
}

// Load 的错误信息必须带上配置文件路径，便于启动排障。
func TestLoadErrorMentionsPath(t *testing.T) {
	p := writeFile(t, "bogus = 1\n")
	_, err := config.Load(p)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), p) {
		t.Fatalf("错误信息应包含配置路径 %q，实际 %v", p, err)
	}
	_ = fmt.Sprint()
}
