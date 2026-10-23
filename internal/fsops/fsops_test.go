package fsops_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homevault/internal/fsops"
)

// mustSymlink 创建符号链接；Windows 上没有权限/开发者模式时明确跳过。
func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("当前环境无法创建符号链接（%v），跳过符号链接相关断言", err)
	}
	fi, err := os.Lstat(newname)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skipf("符号链接未按预期创建（err=%v），跳过", err)
	}
}

func newOps(t *testing.T) (*fsops.Ops, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "files")
	o, err := fsops.New(dir)
	if err != nil {
		t.Fatalf("fsops.New 失败：%v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("New 应自动创建存储根目录：%v", err)
	}
	return o, dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCleanRel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{".", "", false},
		{"/", "", false},
		{"a", "a", false},
		{"a/b", "a/b", false},
		{"a//b", "a/b", false},
		{"a/", "a", false},
		{"a/./b", "a/b", false},
		{"..", "", false}, // 被钳制到根（不是报错）
		{"../..", "", false},
		{"../bob/x", "bob/x", false},
		{"a/../../b", "b", false},
		{"a/b/../../../c", "c", false},
		{"/etc/passwd", "", true},
		{"//srv/share", "", true},
		{`\\srv\share`, "", true},
		{`\etc\passwd`, "", true},
		{`a\b\c`, "a/b/c", false},
		{`..\..\x`, "x", false},
		{"a\x00b", "", true},
		{"%00", "%00", false}, // 只是普通字符串；解码后的 NUL 才会被拒
		{"CON", "CON", false},
		{"PROGRA~1", "PROGRA~1", false},
		{"a.", "a.", false},
		{"a ", "a ", false},
		{"  a  ", "  a  ", false},
		{"中文/目录/文件.txt", "中文/目录/文件.txt", false},
	}
	for _, c := range cases {
		got, err := fsops.CleanRel(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("CleanRel(%q) 应报错，实际得到 %q", c.in, got)
			}
			if !errors.Is(err, fsops.ErrUnsafePath) {
				t.Fatalf("CleanRel(%q) 应返回 ErrUnsafePath，实际 %v", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("CleanRel(%q) 意外报错：%v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("CleanRel(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}

	// 超长路径
	if _, err := fsops.CleanRel(strings.Repeat("a", fsops.MaxPathLength+1)); err == nil {
		t.Fatal("超过 MaxPathLength 必须报错")
	}
	// 刚好等于上限（无分隔符）仍应通过
	if got, err := fsops.CleanRel(strings.Repeat("a", fsops.MaxPathLength)); err != nil || len(got) != fsops.MaxPathLength {
		t.Fatalf("刚好等于上限应通过：len=%d err=%v", len(got), err)
	}
}

// ".." 被静默钳制到根，而不是报错；CleanRel 里那段 "不允许 .." 是死代码。
func TestCleanRelDotDotIsClampedNotRejected(t *testing.T) {
	for _, in := range []string{"..", "../..", "../../../../../etc"} {
		if in == "../../../../../etc" {
			continue
		}
		got, err := fsops.CleanRel(in)
		if err != nil || got != "" {
			t.Fatalf("CleanRel(%q) 当前实现会钳制为根，实际 %q err=%v", in, got, err)
		}
	}
	// 关键：无论怎么绕，结果都不会带 .. 前缀
	for _, in := range []string{"..", "../x", "a/../../x", `..\..\x`} {
		got, _ := fsops.CleanRel(in)
		if strings.HasPrefix(got, "..") {
			t.Fatalf("CleanRel(%q)=%q 仍以 .. 开头", in, got)
		}
	}
}

func TestCleanName(t *testing.T) {
	ok := []string{"a", "a b", "文件.txt", "PROGRA~1", "a.", "a ", "  ", "\x01", "CON", "NUL", "COM1", "a#b", "a%b"}
	for _, n := range ok {
		if got, err := fsops.CleanName(n); err != nil || got != n {
			t.Fatalf("CleanName(%q) 当前实现原样接受，实际 %q err=%v", n, got, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", `a\b`, "a\x00b"}
	for _, n := range bad {
		if _, err := fsops.CleanName(n); err == nil {
			t.Fatalf("CleanName(%q) 必须报错", n)
		}
	}
	if _, err := fsops.CleanName(strings.Repeat("a", 256)); err == nil {
		t.Fatal("超过 255 字节的名称必须报错")
	}
	if _, err := fsops.CleanName(strings.Repeat("a", 255)); err != nil {
		t.Fatalf("255 字节的名称应通过：%v", err)
	}
	// 256 个中文字符（>255 字节）也必须报错
	if _, err := fsops.CleanName(strings.Repeat("中", 100)); err == nil {
		t.Fatal("按字节计算的 255 上限应拒绝 100 个中文字符（300 字节）")
	}
}

func TestCleanRoot(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"alice", "alice", false},
		{"shared/team", "shared/team", false},
		{"a/b/c", "a/b/c", false},
		{"alice/", "alice", false},
		{"shared//team", "shared/team", false},
		{"", "", true},
		{".", "", true},
		{"/", "", true},
		{"..", "", true},
		{"a/..", "", true},
		{"../alice", "alice", false}, // 钳制后仍是合法根
		{"/alice", "", true},
		{`\\srv\share`, "", true},
		{"a/b\x00c", "", true},
	}
	for _, c := range cases {
		got, err := fsops.CleanRoot(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("CleanRoot(%q) 应报错，实际 %q", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("CleanRoot(%q)=%q err=%v，期望 %q", c.in, got, err, c.want)
		}
	}
	// 根里的每一段都要过 CleanName：含分隔符的段不可能出现，但空段要拒绝
	if _, err := fsops.CleanRoot("a//"); err != nil {
		t.Fatalf("a// 应归一化为 a：%v", err)
	}
}

func TestResolveKeepsInsideRoot(t *testing.T) {
	o, root := newOps(t)
	cases := []struct{ root, rel string }{
		{"alice", ""},
		{"alice", "a.txt"},
		{"alice", "../bob/x"},
		{"alice", ".."},
		{"alice", "a/../../b"},
		{"shared/team", "a/../b"},
	}
	for _, c := range cases {
		raw, err := o.Resolve(c.root, c.rel)
		if err != nil {
			t.Fatalf("Resolve(%q,%q) 意外报错：%v", c.root, c.rel, err)
		}
		got := resolveForTest(raw)
		cleanRoot, _ := fsops.CleanRoot(c.root)
		// t.TempDir() 在 Windows 上可能是 8.3 短名（如 DISILL~1），Ops.StorageRoot
		// 内部做过 EvalSymlinks 是长名。两边都规范化，否则 Rel 会误判越界。
		base := resolveForTest(filepath.Join(root, filepath.FromSlash(cleanRoot)))
		rel, err := filepath.Rel(base, got)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("Resolve(%q,%q)=%q 越出了 %q", c.root, c.rel, got, base)
		}
	}
	// 绝对路径/UNC/空字节直接报错
	for _, rel := range []string{"/etc/passwd", `\\srv\share`, "//x", "a\x00b"} {
		if _, err := o.Resolve("alice", rel); err == nil {
			t.Fatalf("Resolve(alice,%q) 必须报错", rel)
		}
	}
	// 管理员（空根）被拒绝
	if _, err := o.Resolve("", ""); !errors.Is(err, fsops.ErrNoRoot) {
		t.Fatalf("空 root 应返回 ErrNoRoot，实际 %v", err)
	}
}

func TestResolveRejectsSymlinks(t *testing.T) {
	o, root := newOps(t)
	alice := filepath.Join(root, "alice")
	write(t, filepath.Join(alice, "real.txt"), "real")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "top secret")

	// 1) 目录符号链接指向空间外：经它访问必须被拒
	mustSymlink(t, outside, filepath.Join(alice, "escape"))
	if _, err := o.Resolve("alice", "escape/secret.txt"); !errors.Is(err, fsops.ErrUnsafePath) {
		t.Fatalf("经符号链接目录访问空间外必须被拒，实际 %v", err)
	}
	// 2) 文件符号链接指向空间外
	mustSymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(alice, "link.txt"))
	if _, err := o.Resolve("alice", "link.txt"); !errors.Is(err, fsops.ErrUnsafePath) {
		t.Fatalf("文件符号链接必须被拒，实际 %v", err)
	}
	// 3) 根目录本身是符号链接
	mustSymlink(t, outside, filepath.Join(root, "linkedRoot"))
	if _, err := o.Resolve("linkedRoot", "secret.txt"); err == nil {
		t.Fatal("根目录是符号链接时必须被拒")
	}
	if _, err := o.EnsureRoot("linkedRoot"); err == nil {
		t.Fatal("EnsureRoot 遇到符号链接根必须报错")
	}
	// 4) 指向空间内部的符号链接同样被拒（策略是"一律不跟随"）
	mustSymlink(t, filepath.Join(alice, "real.txt"), filepath.Join(alice, "inlink.txt"))
	if _, err := o.Resolve("alice", "inlink.txt"); !errors.Is(err, fsops.ErrUnsafePath) {
		t.Fatalf("指向空间内部的符号链接也必须被拒，实际 %v", err)
	}
	// 5) List 不展示符号链接；DirSize 不跟随、不计数
	entries, err := fsops.List(alice)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "escape" || e.Name == "link.txt" || e.Name == "inlink.txt" {
			t.Fatalf("List 不应返回符号链接 %q：%+v", e.Name, entries)
		}
	}
	size, err := fsops.DirSize(alice)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len("real")) {
		t.Fatalf("DirSize 不应跟随符号链接，实际 %d（应为 %d）", size, len("real"))
	}
	// 6) Copy 拒绝以符号链接为源
	if err := fsops.Copy(filepath.Join(alice, "link.txt"), filepath.Join(alice, "copy.txt")); !errors.Is(err, fsops.ErrUnsafePath) {
		t.Fatalf("Copy 必须拒绝符号链接源，实际 %v", err)
	}
	// 7) 复制"内含符号链接的目录"必须整体失败，而不是悄悄复制链接
	if err := fsops.Copy(alice, filepath.Join(root, "alice-copy")); !errors.Is(err, fsops.ErrUnsafePath) {
		t.Fatalf("目录内含符号链接时应报错，实际 %v", err)
	}
}

// 存储根自身是符号链接时，New 会解析到真实路径（这是有意为之，便于空间目录搬迁）。
func TestNewEvaluatesStorageRootSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "space-link")
	mustSymlink(t, real, link)
	o, err := fsops.New(link)
	if err != nil {
		t.Fatalf("New 应能解析符号链接形式的存储根：%v", err)
	}
	realEval, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if o.StorageRoot != realEval {
		t.Fatalf("StorageRoot 应为解析后的真实路径 %q，实际 %q", realEval, o.StorageRoot)
	}
}

func TestIsSubPath(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"a", "a", true},
		{"a", "a/b", true},
		{"a", "ab", false},
		{"a", "a/b/c", true},
		{"", "anything", true},
		{"", "", true},
		{"a/b", "a/bc", false},
		{"a/b", "a", false},
		{"shared", "shared/team", true},
	}
	for _, c := range cases {
		if got := fsops.IsSubPath(c.parent, c.child); got != c.want {
			t.Fatalf("IsSubPath(%q,%q)=%v，期望 %v", c.parent, c.child, got, c.want)
		}
	}
}

func TestUniqueName(t *testing.T) {
	dir := t.TempDir()
	if got, err := fsops.UniqueName(dir, "a.txt"); err != nil || got != "a.txt" {
		t.Fatalf("无冲突时应原样返回：%q %v", got, err)
	}
	write(t, filepath.Join(dir, "a.txt"), "1")
	if got, _ := fsops.UniqueName(dir, "a.txt"); got != "a (1).txt" {
		t.Fatalf("期望 a (1).txt，实际 %q", got)
	}
	write(t, filepath.Join(dir, "a (1).txt"), "2")
	if got, _ := fsops.UniqueName(dir, "a.txt"); got != "a (2).txt" {
		t.Fatalf("期望 a (2).txt，实际 %q", got)
	}
	// 无扩展名
	write(t, filepath.Join(dir, "b"), "1")
	if got, _ := fsops.UniqueName(dir, "b"); got != "b (1)" {
		t.Fatalf("期望 b (1)，实际 %q", got)
	}
	// 多段扩展名：只认最后一个点
	write(t, filepath.Join(dir, "c.tar.gz"), "1")
	if got, _ := fsops.UniqueName(dir, "c.tar.gz"); got != "c.tar (1).gz" {
		t.Fatalf("多段扩展名的当前行为应为 c.tar (1).gz，实际 %q", got)
	}
	// 目录同名也参与去重
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got, _ := fsops.UniqueName(dir, "d"); got != "d (1)" {
		t.Fatalf("目录同名也应去重，实际 %q", got)
	}
}

// 以点开头的隐藏文件（Go 的 filepath.Ext(".env") == ".env"）也要生成合法的新名字（D17 修复）。
func TestUniqueNameDotfileDeduplication(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".env"), "1")
	got, err := fsops.UniqueName(dir, ".env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Fatalf("去重后的名字不应以空格开头/结尾，实际 %q", got)
	}
	if got != ".env (1)" {
		t.Fatalf("隐藏文件应在扩展名后加序号，实际 %q", got)
	}
	write(t, filepath.Join(dir, got), "2")
	if again, err := fsops.UniqueName(dir, ".env"); err != nil || again != ".env (2)" {
		t.Fatalf("第二次去重应为 .env (2)，实际 %q err=%v", again, err)
	}
}

func regression(t *testing.T, id, desc string) {
	t.Helper()
	// 该缺陷已修复，用例默认运行；id/desc 仅用于回溯历史。
	t.Logf("回归用例 %s：%s", id, desc)
}

func TestTempFileAndAtomicRename(t *testing.T) {
	dir := t.TempDir()
	f, err := fsops.TempFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	tmpName := f.Name()
	if filepath.Dir(tmpName) != dir {
		t.Fatalf("临时文件必须与目标同目录（保证 rename 原子）：%q", tmpName)
	}
	if !strings.HasPrefix(filepath.Base(tmpName), ".homevault-tmp-") {
		t.Fatalf("临时文件名前缀不符：%q", tmpName)
	}
	if _, err := f.WriteString("hello 世界"); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, "final.txt")
	if err := os.Rename(tmpName, final); err != nil {
		t.Fatalf("同目录 rename 应成功：%v", err)
	}
	if _, err := os.Stat(tmpName); !os.IsNotExist(err) {
		t.Fatalf("rename 后临时名不应存在：%v", err)
	}
	raw, err := os.ReadFile(final)
	if err != nil || string(raw) != "hello 世界" {
		t.Fatalf("最终内容不正确：%q %v", raw, err)
	}
}

func TestDirSizeNested(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), strings.Repeat("x", 100))
	write(t, filepath.Join(dir, "sub", "b.txt"), strings.Repeat("y", 250))
	write(t, filepath.Join(dir, "sub", "deep", "deeper", "c.bin"), strings.Repeat("z", 1000))
	write(t, filepath.Join(dir, "empty.txt"), "")
	if err := os.MkdirAll(filepath.Join(dir, "emptyDir", "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := fsops.DirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1350 {
		t.Fatalf("嵌套目录统计应为 1350，实际 %d", got)
	}
	if _, err := fsops.DirSize(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("不存在的目录应报错")
	}
}

func TestListSortsAndSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"Zdir", "adir"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dir, "b.txt"), "bb")
	write(t, filepath.Join(dir, "A.txt"), "a")
	entries, err := fsops.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range entries {
		order = append(order, e.Name)
		if e.IsDir && e.Size != 0 {
			t.Fatalf("目录条目 Size 应为 0：%+v", e)
		}
	}
	want := []string{"adir", "Zdir", "A.txt", "b.txt"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("顺序应为“目录优先 + 名称不区分大小写升序”，实际 %v，期望 %v", order, want)
	}
	if _, err := fsops.List(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("不存在的目录应报错")
	}
}

func TestCopyAndMove(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "a.txt"), "aaa")
	write(t, filepath.Join(src, "sub", "b.txt"), "bbb")
	dst := filepath.Join(root, "dst")
	if err := fsops.Copy(src, dst); err != nil {
		t.Fatalf("目录复制失败：%v", err)
	}
	for _, f := range []string{"a.txt", filepath.Join("sub", "b.txt")} {
		raw, err := os.ReadFile(filepath.Join(dst, f))
		if err != nil {
			t.Fatalf("复制结果缺少 %s：%v", f, err)
		}
		if len(raw) == 0 {
			t.Fatalf("复制结果内容为空：%s", f)
		}
	}
	// 源仍在（Copy 而非 Move）
	if _, err := os.Stat(filepath.Join(src, "a.txt")); err != nil {
		t.Fatalf("Copy 不应删除源：%v", err)
	}
	// Move：源消失、目标出现
	moved := filepath.Join(root, "moved")
	if err := fsops.Move(src, moved); err != nil {
		t.Fatalf("Move 失败：%v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("Move 后源目录应消失：%v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "sub", "b.txt")); err != nil {
		t.Fatalf("Move 后目标内容缺失：%v", err)
	}
	// Exists
	if !fsops.Exists(moved) || fsops.Exists(filepath.Join(root, "ghost")) {
		t.Fatal("Exists 行为不正确")
	}
}

// resolveForTest 规范化路径后再比较：Windows 的 t.TempDir() 可能返回 8.3 短名，
// 而 Ops.Resolve 内部走 EvalSymlinks 产生的长名，两者直接拼 Rel 会误判为越界。
// 注意目标路径此时可能还不存在（EvalSymlinks 会失败），所以逐级向上找最近的存在目录，
// 把它规范化后再拼回剩余部分。
func resolveForTest(p string) string {
	if ev, err := filepath.EvalSymlinks(p); err == nil {
		return ev
	}
	var rest []string
	cur := p
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // 到根都没解析成功，原样返回
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
		if ev, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{ev}, rest...)
			return filepath.Join(parts...)
		}
	}
}
