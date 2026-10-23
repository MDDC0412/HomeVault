package core

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"homevault/internal/state"
)

// setOffice 控制 LibreOffice 探测结果：
//   - setting: officePreview（auto/off/builtin）
//   - env: HOMEVAULT_SOFFICE 的值；传一个存在的文件表示"服务端可转换"
//
// 同时清掉 60 秒的全局探测缓存，保证测试确定性（不依赖本机是否真的装了 LibreOffice）。
func (f *fix) setOffice(setting, env string) {
	f.t.Helper()
	sofficeMu.Lock()
	sofficeChecked = false
	sofficePath = ""
	sofficeMu.Unlock()
	f.t.Setenv("HOMEVAULT_SOFFICE", env)
	st := state.DefaultSettings()
	st.OfficePreview = setting
	f.putSettings(st)
}

func (f *fix) fakeSoffice() string {
	f.t.Helper()
	p := filepath.Join(f.cfg.SpaceDir, "fake-soffice.exe")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o640); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestPlanPreviewByKind(t *testing.T) {
	f := newFix(t)
	f.setOffice("off", "") // 服务端不转换，走内置渲染器
	f.ensureRoot(f.alice)
	f.write(f.alice, "pic.png", "x")
	f.write(f.alice, "doc.pdf", "x")
	f.write(f.alice, "note.txt", "x")
	f.write(f.alice, "movie.mp4", "x")
	f.write(f.alice, "sound.mp3", "x")
	f.write(f.alice, "sheet.xlsx", "x")
	f.write(f.alice, "bundle.zip", "x")
	f.write(f.alice, "blob.bin", "x")
	f.svc.Mkdir(f.act(), f.alice, "", "folder")

	cases := []struct {
		rel   string
		kind  string
		mode  string
		mime  string
		built bool
	}{
		{"pic.png", "image", "inline", "image/png", false},
		{"doc.pdf", "pdf", "inline", "application/pdf", false},
		{"note.txt", "text", "inline", "text/plain; charset=utf-8", false},
		{"movie.mp4", "video", "inline", "video/mp4", false},
		{"sound.mp3", "audio", "inline", "audio/mpeg", false},
		{"sheet.xlsx", "office", "convert",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", true},
		{"bundle.zip", "archive", "download", "application/zip", false},
		{"blob.bin", "other", "download", "application/octet-stream", false},
	}
	for _, c := range cases {
		plan, err := f.svc.PlanPreview(f.alice, c.rel)
		if err != nil {
			t.Fatalf("PlanPreview(%s): %v", c.rel, err)
		}
		if plan.Kind != c.kind || plan.Mode != c.mode || plan.Builtin != c.built {
			t.Fatalf("PlanPreview(%s)=%+v，期望 kind=%s mode=%s builtin=%v", c.rel, plan, c.kind, c.mode, c.built)
		}
		if plan.MimeType != c.mime {
			t.Fatalf("PlanPreview(%s) mime=%q，期望 %q", c.rel, plan.MimeType, c.mime)
		}
		if plan.Ext != ExtOf(c.rel) {
			t.Fatalf("PlanPreview(%s) ext=%q", c.rel, plan.Ext)
		}
		if plan.Size <= 0 {
			t.Fatalf("PlanPreview(%s) size=%d", c.rel, plan.Size)
		}
	}
	if _, err := f.svc.PlanPreview(f.alice, "folder"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("目录预览应报 ErrBadRequest，实际 %v", err)
	}
	if _, err := f.svc.PlanPreview(f.alice, "ghost.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("文件不存在应报 ErrNotFound，实际 %v", err)
	}
	if _, err := f.svc.PlanPreview(f.admin, "pic.png"); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("管理员应报 ErrNoStorage，实际 %v", err)
	}
}

func TestOfficePreviewSettingSemantics(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.write(f.alice, "sheet.xlsx", "x")

	// off：无论本机是否装了 LibreOffice，都不做服务端转换
	f.setOffice("off", f.fakeSoffice())
	if f.svc.OfficeAvailable() {
		t.Fatal("officePreview=off 时不应报告 LibOffice 可用")
	}
	plan, err := f.svc.PlanPreview(f.alice, "sheet.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "convert" || !plan.Builtin {
		t.Fatalf("off 时 office 预览应为内置渲染（mode=convert + builtin），实际 %+v", plan)
	}
	if _, err := f.svc.EnsureOfficePDF(f.alice, "sheet.xlsx"); !errors.Is(err, ErrPreviewUnavailable) {
		t.Fatalf("off 时服务端转换应返回 ErrPreviewUnavailable，实际 %v", err)
	}

	// auto + 可用的 soffice：服务端转换
	f.setOffice("auto", f.fakeSoffice())
	if !f.svc.OfficeAvailable() {
		t.Fatal("指向存在的文件时应报告可用")
	}
	plan, err = f.svc.PlanPreview(f.alice, "sheet.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Builtin {
		t.Fatalf("可服务端转换时不应要求内置渲染，实际 %+v", plan)
	}

	// off 会覆盖 HOMEVAULT_SOFFICE
	f.setOffice("off", f.fakeSoffice())
	if f.svc.OfficeAvailable() {
		t.Fatal("off 必须优先于 HOMEVAULT_SOFFICE")
	}
}

// officePreview 三种取值的契约（D13 修复）：
// off / builtin 都不探测 soffice（一律交前端内置渲染），只有 auto 才会在服务端
// 存在 LibreOffice 时做转换。
func TestOfficePreviewModes(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.write(f.alice, "sheet.xlsx", "x")

	// builtin：即使存在 soffice，也必须走内置渲染
	f.setOffice("builtin", f.fakeSoffice())
	if f.svc.OfficeAvailable() {
		t.Fatal("officePreview=builtin 不应探测/启用服务端转换")
	}
	plan, err := f.svc.PlanPreview(f.alice, "sheet.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Builtin {
		t.Fatalf("officePreview=builtin 应强制内置渲染（Builtin=true），实际 %+v", plan)
	}

	// off：同样不转换
	f.setOffice("off", f.fakeSoffice())
	if f.svc.OfficeAvailable() {
		t.Fatal("officePreview=off 必须优先于 HOMEVAULT_SOFFICE")
	}

	// auto + 存在 soffice：服务端转换，Builtin=false
	f.setOffice("auto", f.fakeSoffice())
	if !f.svc.OfficeAvailable() {
		t.Skip("前提不成立：模拟的 soffice 未被识别")
	}
	plan, err = f.svc.PlanPreview(f.alice, "sheet.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Builtin || plan.Mode != "convert" {
		t.Fatalf("auto+soffice 应走服务端转换，实际 %+v", plan)
	}
}

func TestMimeOf(t *testing.T) {
	cases := map[string]string{
		"a.PNG":  "image/png",
		"b.docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"c.mp4":  "video/mp4",
		"d.xyz":  "application/octet-stream",
		"noext":  "application/octet-stream",
	}
	for name, want := range cases {
		if got := MimeOf(name); got != want {
			t.Fatalf("MimeOf(%q)=%q，期望 %q", name, got, want)
		}
	}
}

func TestReadTextPreview(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.write(f.alice, "a.txt", "hello 世界")
	got, err := f.svc.ReadTextPreview(f.alice, "a.txt")
	if err != nil || got != "hello 世界" {
		t.Fatalf("ReadTextPreview=%q err=%v", got, err)
	}
	// 超过上限时只返回前 TextPreviewLimit 字节
	f.writeSize(f.alice, "big.txt", int64(TextPreviewLimit)+10)
	got, err = f.svc.ReadTextPreview(f.alice, "big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != TextPreviewLimit {
		t.Fatalf("应截断到 %d 字节，实际 %d", TextPreviewLimit, len(got))
	}
	if _, err := f.svc.ReadTextPreview(f.alice, "ghost.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("文件不存在应报 ErrNotFound，实际 %v", err)
	}
	if _, err := f.svc.ReadTextPreview(f.alice, "sub"); err == nil {
		t.Fatal("对目录读取文本预览应当报错")
	}
}

// 空文件会返回 io.EOF 而不是空字符串（当前没有任何 HTTP 接口调用它，属潜在缺陷）。
func TestReadTextPreviewEmptyFileReturnsEOF(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.write(f.alice, "empty.txt", "")
	got, err := f.svc.ReadTextPreview(f.alice, "empty.txt")
	if got != "" || !errors.Is(err, io.EOF) {
		t.Fatalf("当前实现返回 (%q, %v)：空文件应视为空内容而不是错误", got, err)
	}
}

func TestCleanupCache(t *testing.T) {
	f := newFix(t)
	cacheDir := filepath.Join(f.cfg.CachePath(), "office")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(cacheDir, "old.pdf")
	fresh := filepath.Join(cacheDir, "fresh.pdf")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("0123456789"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, twoHoursAgo, twoHoursAgo); err != nil {
		t.Fatal(err)
	}
	n, bytes, err := f.svc.CleanupCache(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || bytes != 10 {
		t.Fatalf("应清理 1 个 10 字节文件，实际 %d/%d", n, bytes)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("旧缓存应被删除：%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("新缓存不应被删除：%v", err)
	}
	// maxAge<=0 使用 24h 默认值：2 小时前的文件应保留
	n, _, err = f.svc.CleanupCache(0)
	if err != nil || n != 0 {
		t.Fatalf("maxAge=0 应回落到 24 小时并保留 2 小时前的文件：%d %v", n, err)
	}
	// 缓存目录不存在时不应报错
	if err := os.RemoveAll(f.cfg.CachePath()); err != nil {
		t.Fatal(err)
	}
	if n, _, err := f.svc.CleanupCache(time.Hour); err != nil || n != 0 {
		t.Fatalf("缓存目录不存在应返回 0/nil，实际 %d %v", n, err)
	}
}
