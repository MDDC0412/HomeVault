package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homevault/internal/audit"
	"homevault/internal/auth"
	"homevault/internal/config"
	"homevault/internal/fsops"
	"homevault/internal/state"
)

// regression：这些用例原本用于复现已知缺陷；缺陷修复后改为默认运行的回归用例，id/desc 只用于回溯。
func regression(t *testing.T, id, desc string) {
	t.Helper()
	// 该缺陷已修复，用例默认运行；id/desc 仅用于回溯历史。
	t.Logf("回归用例 %s：%s", id, desc)
}

type fix struct {
	t     *testing.T
	cfg   *config.Config
	st    *state.Store
	svc   *Service
	logs  *audit.Logger
	admin *state.User
	alice *state.User
	bob   *state.User
}

func allPerms() map[string]bool {
	m := map[string]bool{}
	for _, p := range state.UserPermissions {
		m[p] = true
	}
	return m
}

func newFix(t *testing.T) *fix {
	t.Helper()
	space := filepath.Join(t.TempDir(), "space")
	cfg := &config.Config{Listen: "127.0.0.1:0", SpaceDir: space}
	if err := cfg.EnsureLayout(); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	st, err := state.Open(cfg.StatePath())
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if _, err := st.EnsureSettings(state.DefaultSettings()); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	logs, err := audit.New(cfg.AuditPath())
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	fso, err := fsops.New(cfg.FilesPath())
	if err != nil {
		t.Fatalf("fsops.New: %v", err)
	}
	tokens, err := auth.NewTokenManager(time.Hour)
	if err != nil {
		t.Fatalf("auth.NewTokenManager: %v", err)
	}
	f := &fix{
		t: t, cfg: cfg, st: st, logs: logs,
		svc: New(cfg, st, fso, logs, tokens),
		admin: &state.User{Username: "admin", IsAdmin: true, Permissions: map[string]bool{},
			PasswordHash: "hash-admin"},
		alice: &state.User{Username: "alice", Root: "alice", Quota: 64 << 20, Permissions: allPerms(),
			PasswordHash: "hash-alice"},
		bob: &state.User{Username: "bob", Root: "bob", Quota: 64 << 20, Permissions: allPerms(),
			PasswordHash: "hash-bob"},
	}
	for _, u := range []*state.User{f.admin, f.alice, f.bob} {
		if err := st.PutUser(u); err != nil {
			t.Fatalf("PutUser(%s): %v", u.Username, err)
		}
	}
	return f
}

func (f *fix) act() Actor {
	return Actor{Username: "admin", IP: "127.0.0.1", UserAgent: "test"}
}

func (f *fix) actAs(name string) Actor {
	return Actor{Username: name, ActingAs: name, IP: "127.0.0.1"}
}

// write 直接在用户根里落一个文件（绕过服务层，用于准备数据）。
func (f *fix) write(u *state.User, rel, content string) {
	f.t.Helper()
	abs, err := f.svc.Resolve(u, rel)
	if err != nil {
		f.t.Fatalf("Resolve(%s): %v", rel, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o640); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fix) writeSize(u *state.User, rel string, n int64) string {
	f.t.Helper()
	abs, err := f.svc.Resolve(u, rel)
	if err != nil {
		f.t.Fatalf("Resolve(%s): %v", rel, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		f.t.Fatal(err)
	}
	fh, err := os.Create(abs)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := fh.Truncate(n); err != nil {
		fh.Close()
		f.t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		f.t.Fatal(err)
	}
	return abs
}

func (f *fix) read(u *state.User, rel string) (string, error) {
	f.t.Helper()
	abs, err := f.svc.Resolve(u, rel)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(abs)
	return string(raw), err
}

func (f *fix) ensureRoot(u *state.User) string {
	f.t.Helper()
	p, err := f.svc.RootAbs(u)
	if err != nil {
		f.t.Fatalf("RootAbs(%s): %v", u.Username, err)
	}
	return p
}

func (f *fix) putSettings(s state.Settings) {
	f.t.Helper()
	if _, err := f.st.PutSettings(s); err != nil {
		f.t.Fatalf("PutSettings: %v", err)
	}
}

func TestRootAbsAndUsageOnEmptySpace(t *testing.T) {
	f := newFix(t)
	if _, err := f.svc.RootAbs(f.admin); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("管理员没有自己的空间，应返回 ErrNoStorage，实际 %v", err)
	}
	if _, err := f.svc.RootAbs(nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("nil 用户应返回 ErrForbidden，实际 %v", err)
	}
	root := f.ensureRoot(f.alice)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("RootAbs 应创建用户根目录：%v", err)
	}
	if _, err := f.svc.Resolve(f.admin, "x"); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("Resolve 对管理员也应返回 ErrNoStorage，实际 %v", err)
	}
	used, trash, err := f.svc.Usage(f.alice)
	if err != nil || used != 0 || trash != 0 {
		t.Fatalf("空空间用量应为 0/0，实际 %d/%d err=%v", used, trash, err)
	}
	// 管理员用量同样被拒
	if _, _, err := f.svc.Usage(f.admin); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("管理员 Usage 应被拒，实际 %v", err)
	}
}

func TestCheckQuotaRules(t *testing.T) {
	f := newFix(t)
	// Quota=0 表示不限
	f.alice.Quota = 0
	if err := f.svc.CheckQuota(f.alice, 1<<40); err != nil {
		t.Fatalf("配额 0 应视为不限：%v", err)
	}
	// 配额正好用满
	f.alice.Quota = 1000
	f.write(f.alice, "a.bin", strings.Repeat("x", 600))
	if err := f.svc.CheckQuota(f.alice, 400); err != nil {
		t.Fatalf("600+400=1000 应允许：%v", err)
	}
	if err := f.svc.CheckQuota(f.alice, 401); !errors.Is(err, ErrQuota) {
		t.Fatalf("超过 1 字节必须报 ErrQuota，实际 %v", err)
	}
	// 回收站占用参与计算
	if err := f.st.PutTrashEntry(&state.TrashEntry{ID: "t1", Root: "alice", Size: 100, Name: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CheckQuota(f.alice, 300); err != nil {
		t.Fatalf("600+100+300=1000 应允许：%v", err)
	}
	if err := f.svc.CheckQuota(f.alice, 301); !errors.Is(err, ErrQuota) {
		t.Fatalf("含回收站后超出必须报 ErrQuota，实际 %v", err)
	}
	// 别人的回收站不影响 alice
	f.alice.Quota = 700
	if err := f.svc.CheckQuota(f.alice, 0); err != nil {
		t.Fatalf("quota=700 时 600+100 应放行：%v", err)
	}
	if err := f.st.PutTrashEntry(&state.TrashEntry{ID: "t2", Root: "bob", Size: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CheckQuota(f.alice, 0); err != nil {
		t.Fatalf("别人的回收站不应计入 alice：%v", err)
	}
}

// CheckQuota 必须用实时用量（D5 修复）：Usage() 仍走 5 秒缓存（性能考虑），
// 但配额校验不能相信缓存，否则缓存窗口内的并发写可以突破配额。
func TestCheckQuotaIgnoresUsageCache(t *testing.T) {
	f := newFix(t)
	f.alice.Quota = 1000
	f.ensureRoot(f.alice)
	if _, _, err := f.svc.Usage(f.alice); err != nil { // 预热缓存
		t.Fatal(err)
	}
	// 缓存有效期内磁盘已经多了 900 字节
	f.write(f.alice, "sneaky.bin", strings.Repeat("y", 900))
	// 公开的 Usage 在缓存窗口内返回旧值是允许的（这是缓存的意义）
	if used, _, err := f.svc.Usage(f.alice); err != nil || used != 0 {
		t.Fatalf("缓存窗口内 Usage 返回旧值是允许的，实际 used=%d err=%v", used, err)
	}
	// 但配额校验必须看到真实占用：900+900=1800 > 1000
	if err := f.svc.CheckQuota(f.alice, 900); !errors.Is(err, ErrQuota) {
		t.Fatalf("CheckQuota 必须看到真实占用并拒绝，实际 %v", err)
	}
	if err := f.svc.CheckQuota(f.alice, 50); err != nil {
		t.Fatalf("900+50=950 应放行：%v", err)
	}
	// 失效缓存后 Usage 也回到真实值
	f.svc.InvalidateUsage(f.alice.Root)
	if used, _, err := f.svc.Usage(f.alice); err != nil || used != 900 {
		t.Fatalf("失效缓存后 Usage 应为 900，实际 %d err=%v", used, err)
	}
}

func TestResolveSafety(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.ensureRoot(f.bob)
	f.write(f.bob, "secret.txt", "bob-secret")

	cases := []string{"../bob/secret.txt", "..", "a/../../bob/secret.txt", "....//bob/secret.txt"}
	for _, rel := range cases {
		abs, err := f.svc.Resolve(f.alice, rel)
		if err != nil {
			continue // 报错也是可接受的
		}
		// Windows 上 TempDir 可能返回 8.3 短名（DISILL~1），而 Resolve 内部做了
		// EvalSymlinks 得到长名，直接比对会误判越界。两侧都规范化后再比。
		aliceRoot := evalPath(filepath.Join(f.cfg.FilesPath(), "alice"))
		r, err := filepath.Rel(aliceRoot, abs)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			t.Fatalf("Resolve(alice,%q)=%q 越出了 alice 的根", rel, abs)
		}
	}
	for _, rel := range []string{"/etc/passwd", `\\server\share`, "//x", "a\x00b"} {
		if _, err := f.svc.Resolve(f.alice, rel); err == nil {
			t.Fatalf("Resolve(alice,%q) 必须报错", rel)
		}
	}
	// List 越界同样不会读到别人的文件
	if _, entries, err := f.svc.List(f.alice, ".."); err == nil {
		for _, e := range entries {
			if e.Name == "secret.txt" {
				t.Fatal("List(alice,\"..\") 读到了 bob 的文件")
			}
		}
	}
	if _, _, err := f.svc.List(f.alice, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("列不存在的目录应返回 ErrNotFound，实际 %v", err)
	}
	f.write(f.alice, "f.txt", "x")
	if _, _, err := f.svc.List(f.alice, "f.txt"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("对文件调用 List 应返回 ErrBadRequest，实际 %v", err)
	}
}

func TestMkdirAndRename(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	if got, err := f.svc.Mkdir(f.act(), f.alice, "", "docs"); err != nil || got != "docs" {
		t.Fatalf("Mkdir 失败：%q %v", got, err)
	}
	if _, err := f.svc.Mkdir(f.act(), f.alice, "", "docs"); !errors.Is(err, ErrExists) {
		t.Fatalf("重名目录应返回 ErrExists，实际 %v", err)
	}
	if _, err := f.svc.Mkdir(f.act(), f.alice, "", "a/b"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("名字含分隔符应返回 ErrBadRequest，实际 %v", err)
	}
	if _, err := f.svc.Mkdir(f.act(), f.alice, "missing", "sub"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("父目录不存在应返回 ErrNotFound，实际 %v", err)
	}
	if got, err := f.svc.Mkdir(f.act(), f.alice, "docs", "sub"); err != nil || got != "docs/sub" {
		t.Fatalf("嵌套 Mkdir 失败：%q %v", got, err)
	}

	f.write(f.alice, "a.txt", "aaa")
	if got, err := f.svc.Rename(f.act(), f.alice, "a.txt", "b.txt"); err != nil || got != "b.txt" {
		t.Fatalf("Rename 失败：%q %v", got, err)
	}
	if _, err := f.read(f.alice, "a.txt"); err == nil {
		t.Fatal("旧名字应已不存在")
	}
	if got, err := f.svc.Rename(f.act(), f.alice, "b.txt", "b.txt"); err != nil || got != "b.txt" {
		t.Fatalf("重命名为同名应幂等：%q %v", got, err)
	}
	f.write(f.alice, "c.txt", "ccc")
	if _, err := f.svc.Rename(f.act(), f.alice, "b.txt", "c.txt"); !errors.Is(err, ErrExists) {
		t.Fatalf("目标已存在应返回 ErrExists，实际 %v", err)
	}
	if _, err := f.svc.Rename(f.act(), f.alice, "", "root2"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("重命名根目录应返回 ErrBadRequest，实际 %v", err)
	}
	if _, err := f.svc.Rename(f.act(), f.alice, "ghost.txt", "x.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重命名不存在的条目应返回 ErrNotFound，实际 %v", err)
	}
	if _, err := f.svc.Rename(f.act(), f.alice, "docs", "new name"); err != nil {
		t.Fatalf("目录重命名应成功：%v", err)
	}
	if _, entries, err := f.svc.List(f.alice, ""); err != nil {
		t.Fatal(err)
	} else {
		names := map[string]bool{}
		for _, e := range entries {
			names[e.Name] = true
		}
		if !names["new name"] || names["docs"] {
			t.Fatalf("重命名结果不正确：%v", names)
		}
	}
}

func TestMoveCopyRules(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.svc.Mkdir(f.act(), f.alice, "", "src")
	f.svc.Mkdir(f.act(), f.alice, "", "dst")
	f.write(f.alice, "src/a.txt", "aaa")
	f.write(f.alice, "src/sub/b.txt", "bbb")

	// 复制目录 → 自动加“- 副本”
	out, err := f.svc.Copy(f.act(), f.alice, []string{"src"}, "dst")
	if err != nil {
		t.Fatalf("Copy 失败：%v", err)
	}
	if len(out) != 1 || out[0] != "dst/src - 副本" {
		t.Fatalf("复制目录应生成 dst/src - 副本，实际 %v", out)
	}
	if raw, err := f.read(f.alice, "dst/src - 副本/sub/b.txt"); err != nil || raw != "bbb" {
		t.Fatalf("复制内容不正确：%q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.FilesPath(), "alice", "src", "a.txt")); err != nil {
		t.Fatalf("Copy 不应删除源：%v", err)
	}
	// 再复制一次 → 序号递增
	out, err = f.svc.Copy(f.act(), f.alice, []string{"src"}, "dst")
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != "dst/src - 副本 (1)" {
		t.Fatalf("第二次复制应加序号，实际 %v", out)
	}
	// 文件复制（同目录）
	out, err = f.svc.Copy(f.act(), f.alice, []string{"src/a.txt"}, "src")
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != "src/a - 副本.txt" {
		t.Fatalf("文件副本名不正确：%v", out)
	}
	if raw, _ := f.read(f.alice, "src/a - 副本.txt"); raw != "aaa" {
		t.Fatalf("文件副本内容不正确：%q", raw)
	}

	// Move 绝不覆盖同名目标
	if _, err := f.svc.Move(f.act(), f.alice, []string{"src/a.txt"}, "src"); !errors.Is(err, ErrExists) {
		t.Fatalf("Move 覆盖同名目标必须被拒，实际 %v", err)
	}
	f.write(f.alice, "dst/a.txt", "existing")
	if _, err := f.svc.Move(f.act(), f.alice, []string{"src/a.txt"}, "dst"); !errors.Is(err, ErrExists) {
		t.Fatalf("目标目录已有同名文件必须被拒，实际 %v", err)
	}
	out, err = f.svc.Move(f.act(), f.alice, []string{"src/a.txt"}, "dst")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("仍然应被拒：%v", err)
	}

	// 不能移动到自身或子目录
	if _, err := f.svc.Move(f.act(), f.alice, []string{"src"}, "src"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("移动到自身必须被拒，实际 %v", err)
	}
	f.svc.Mkdir(f.act(), f.alice, "src", "inner")
	if _, err := f.svc.Move(f.act(), f.alice, []string{"src"}, "src/inner"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("移动到子目录必须被拒，实际 %v", err)
	}
	if _, err := f.svc.Copy(f.act(), f.alice, []string{"src"}, "src"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("复制到自身必须被拒，实际 %v", err)
	}

	// 参数校验
	if _, err := f.svc.Move(f.act(), f.alice, nil, "dst"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("空列表应返回 ErrBadRequest，实际 %v", err)
	}
	tooMany := make([]string, 501)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("f%d", i)
	}
	if _, err := f.svc.Move(f.act(), f.alice, tooMany, "dst"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("超过 500 个条目应返回 ErrBadRequest，实际 %v", err)
	}
	if _, err := f.svc.Move(f.act(), f.alice, []string{"src/a.txt"}, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("目标目录不存在应返回 ErrNotFound，实际 %v", err)
	}
	if _, err := f.svc.Move(f.act(), f.alice, []string{"ghost.txt"}, "dst"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("源不存在应返回 ErrNotFound，实际 %v", err)
	}
	// 根目录不可操作
	if _, err := f.svc.Move(f.act(), f.alice, []string{""}, "dst"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("操作根目录应返回 ErrBadRequest，实际 %v", err)
	}

	// 复制目录要按整棵子树计费配额
	f.alice.Quota = 5
	if _, err := f.svc.Copy(f.act(), f.alice, []string{"src"}, "dst"); !errors.Is(err, ErrQuota) {
		t.Fatalf("复制超配额应返回 ErrQuota，实际 %v", err)
	}
}

func TestTrashLifecycle(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.ensureRoot(f.bob)
	f.write(f.alice, "docs/a.txt", "hello")

	entries, err := f.svc.Trash(f.act(), f.alice, []string{"docs/a.txt"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("Trash 失败：%v %v", entries, err)
	}
	e := entries[0]
	if e.Root != "alice" || e.OriginalPath != "docs/a.txt" || e.Name != "a.txt" || e.Size != 5 || e.IsDir {
		t.Fatalf("回收站记录字段不正确：%+v", e)
	}
	if e.ExpiresAt == nil || !e.ExpiresAt.After(time.Now()) {
		t.Fatalf("默认保留期应给出未来的过期时间：%+v", e.ExpiresAt)
	}
	if _, err := f.read(f.alice, "docs/a.txt"); err == nil {
		t.Fatal("文件应已从原位置移走")
	}
	payload := filepath.Join(f.cfg.TrashPath(), e.ID, "payload")
	if raw, err := os.ReadFile(payload); err != nil || string(raw) != "hello" {
		t.Fatalf("回收站 payload 不正确：%q %v", raw, err)
	}
	if used, trash, err := f.svc.Usage(f.alice); err != nil || used != 0 || trash != 5 {
		t.Fatalf("移入回收站后用量应为 0/5，实际 %d/%d err=%v", used, trash, err)
	}

	// 可见性
	if got := f.svc.VisibleTrash(f.alice); len(got) != 1 {
		t.Fatalf("alice 应看到 1 条，实际 %d", len(got))
	}
	if got := f.svc.VisibleTrash(f.bob); len(got) != 0 {
		t.Fatalf("bob 不应看到 alice 的回收站，实际 %d", len(got))
	}
	if got := f.svc.VisibleTrash(f.admin); len(got) != 1 {
		t.Fatalf("管理员应看到全部，实际 %d", len(got))
	}

	// 越权恢复
	if _, failed, _ := f.svc.Restore(f.actAs("bob"), f.bob, []string{e.ID}); len(failed) != 1 {
		t.Fatalf("bob 不应能恢复 alice 的条目：%+v", failed)
	}
	// 正常恢复
	restored, failed, err := f.svc.Restore(f.actAs("alice"), f.alice, []string{e.ID})
	if err != nil || len(restored) != 1 || len(failed) != 0 {
		t.Fatalf("Restore 失败：%v %v %v", restored, failed, err)
	}
	if raw, err := f.read(f.alice, "docs/a.txt"); err != nil || raw != "hello" {
		t.Fatalf("恢复后内容不正确：%q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.TrashPath(), e.ID)); !os.IsNotExist(err) {
		t.Fatalf("恢复后回收站目录应被清理：%v", err)
	}

	// 恢复冲突：原位置又有同名文件
	entries, err = f.svc.Trash(f.act(), f.alice, []string{"docs/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f.write(f.alice, "docs/a.txt", "new-file")
	restored, failed, err = f.svc.Restore(f.actAs("alice"), f.alice, []string{entries[0].ID})
	if err != nil || len(restored) != 0 || len(failed) != 1 {
		t.Fatalf("冲突时应失败且保留条目：%v %v %v", restored, failed, err)
	}
	if !strings.Contains(failed[0].Error, "同名") {
		t.Fatalf("失败原因应说明同名冲突：%+v", failed[0])
	}
	if _, ok := f.st.TrashEntry(entries[0].ID); !ok {
		t.Fatal("恢复失败后回收站条目不应被删除")
	}
	// 恢复不存在的记录
	if _, failed, _ := f.svc.Restore(f.actAs("alice"), f.alice, []string{"ghost"}); len(failed) != 1 {
		t.Fatal("恢复不存在的记录应记入失败")
	}
}

func TestTrashAndPurgePermissions(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.ensureRoot(f.bob)
	f.write(f.alice, "a.txt", "aaa")
	f.write(f.bob, "b.txt", "bb")

	ae, err := f.svc.Trash(f.act(), f.alice, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Trash(f.act(), f.bob, []string{"b.txt"}); err != nil {
		t.Fatal(err)
	}
	// 越权 Purge
	if _, err := f.svc.Purge(f.actAs("bob"), f.bob, []string{ae[0].ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob 不应能永久删除 alice 的条目，实际 %v", err)
	}
	if _, err := f.svc.Purge(f.actAs("bob"), f.bob, []string{"ghost"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purge 不存在的条目应返回 ErrNotFound，实际 %v", err)
	}
	// bob 清空回收站只影响自己
	n, bytes, err := f.svc.EmptyTrash(f.actAs("bob"), f.bob)
	if err != nil || n != 1 || bytes != 2 {
		t.Fatalf("bob 清空应删除 1 条 2 字节，实际 %d/%d err=%v", n, bytes, err)
	}
	if _, ok := f.st.TrashEntry(ae[0].ID); !ok {
		t.Fatal("alice 的条目被误删")
	}
	// 管理员可以代为清理
	if _, err := f.svc.Purge(f.act(), f.admin, []string{ae[0].ID}); err != nil {
		t.Fatalf("管理员 Purge 失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.TrashPath(), ae[0].ID)); !os.IsNotExist(err) {
		t.Fatalf("purge 后目录应被删除：%v", err)
	}
	if _, _, err := f.svc.Usage(f.alice); err != nil {
		t.Fatal(err)
	}
	// Trash 参数校验
	if _, err := f.svc.Trash(f.act(), f.alice, nil); !errors.Is(err, ErrBadRequest) {
		t.Fatal("空列表应报错")
	}
	if _, err := f.svc.Trash(f.act(), f.alice, []string{""}); !errors.Is(err, ErrBadRequest) {
		t.Fatal("删除根目录应报错")
	}
	if _, err := f.svc.Trash(f.act(), f.alice, []string{"ghost"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("删除不存在的条目应报 ErrNotFound")
	}
	tooMany := make([]string, 501)
	if _, err := f.svc.Trash(f.act(), f.alice, tooMany); !errors.Is(err, ErrBadRequest) {
		t.Fatal("超过 500 个条目应报错")
	}
}

// 配额刚好用满时也必须能删除：文件只是从根目录挪进回收站，used+trash 总量不变（D3 修复）。
func TestTrashAtFullQuotaIsAllowed(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 100
	f.write(f.alice, "big.bin", strings.Repeat("z", 100))
	if err := f.svc.CheckQuota(f.alice, 0); err != nil {
		t.Fatalf("前提：应刚好用满不超限：%v", err)
	}
	if _, err := f.svc.Trash(f.act(), f.alice, []string{"big.bin"}); err != nil {
		t.Fatalf("配额用满时删除不应被拒绝，实际 %v", err)
	}
	// 删除后：根目录 0 字节，回收站 100 字节，总额不变
	used, trash, err := f.svc.Usage(f.alice)
	if err != nil || used != 0 || trash != 100 {
		t.Fatalf("删除后应为 used=0 trash=100，实际 used=%d trash=%d err=%v", used, trash, err)
	}
	if err := f.svc.CheckQuota(f.alice, 0); err != nil {
		t.Fatalf("移入回收站不应改变总额：%v", err)
	}
}

// Restore 不做任何配额校验（文档里也承认了这一点）。
func TestRestoreIgnoresQuota(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 100
	f.write(f.alice, "a.bin", strings.Repeat("a", 50))
	entries, err := f.svc.Trash(f.act(), f.alice, []string{"a.bin"})
	if err != nil {
		t.Fatal(err)
	}
	// 回收站里已有 50 字节；再让磁盘上出现 150 字节（模拟其它途径写入）
	f.write(f.alice, "b.bin", strings.Repeat("b", 150))
	f.svc.InvalidateUsage(f.alice.Root)
	if err := f.svc.CheckQuota(f.alice, 0); !errors.Is(err, ErrQuota) {
		t.Fatalf("前提：此时已超配额（150+50>100），实际 %v", err)
	}
	// 超配额状态下依然能恢复 → 恢复后总量 200 已超限
	restored, failed, err := f.svc.Restore(f.actAs("alice"), f.alice, []string{entries[0].ID})
	if err != nil || len(restored) != 1 || len(failed) != 0 {
		t.Fatalf("当前实现不校验配额，恢复应当成功：%v %v %v", restored, failed, err)
	}
	if used, trash, _ := f.svc.Usage(f.alice); used != 200 || trash != 0 {
		t.Fatalf("恢复后应为 used=200/trash=0，实际 %d/%d", used, trash)
	}
}

func TestPurgeExpiredTrash(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	for _, e := range []*state.TrashEntry{
		{ID: "old", Root: "alice", Name: "old", Size: 10, ExpiresAt: &past},
		{ID: "new", Root: "alice", Name: "new", Size: 20, ExpiresAt: &future},
		{ID: "never", Root: "alice", Name: "never", Size: 30},
	} {
		if err := f.st.PutTrashEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	n, bytes, err := f.svc.PurgeExpiredTrash()
	if err != nil || n != 1 || bytes != 10 {
		t.Fatalf("应只清理 1 条 10 字节，实际 %d/%d err=%v", n, bytes, err)
	}
	if _, ok := f.st.TrashEntry("old"); ok {
		t.Fatal("过期条目应被删除")
	}
	for _, id := range []string{"new", "never"} {
		if _, ok := f.st.TrashEntry(id); !ok {
			t.Fatalf("%s 不应被清理", id)
		}
	}
	// 审计里应留下 system 的清理记录
	res, err := f.logs.Query(audit.Query{Action: audit.ActionCleanup})
	if err != nil || res.Total != 1 {
		t.Fatalf("应写入 1 条清理审计，实际 %d err=%v", res.Total, err)
	}
	if res.Entries[0].User != "system" {
		t.Fatalf("清理审计的 User 应为 system，实际 %q", res.Entries[0].User)
	}
}

func TestAuditRecordsWrites(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.svc.Mkdir(f.actAs("alice"), f.alice, "", "d")
	f.write(f.alice, "d/a.txt", "x")
	f.svc.Rename(f.actAs("alice"), f.alice, "d/a.txt", "b.txt")
	f.svc.Copy(f.actAs("alice"), f.alice, []string{"d"}, "")
	if _, err := f.svc.Trash(f.actAs("alice"), f.alice, []string{"d/b.txt"}); err != nil {
		t.Fatalf("Trash: %v", err)
	}

	res, err := f.logs.Query(audit.Query{User: "alice", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, e := range res.Entries {
		got[e.Action]++
	}
	for _, a := range []string{audit.ActionMkdir, audit.ActionRename, audit.ActionCopy, audit.ActionDelete} {
		if got[a] == 0 {
			t.Fatalf("缺少 %s 的审计记录：%+v", a, got)
		}
	}
}
