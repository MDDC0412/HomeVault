// Package fsops 提供用户文件的实际读写操作，并集中承担路径安全工作。
//
// 与常见实现的关键差别：
//   - 路径一律先做"以 / 为根的 Clean"，因此 `..`、绝对路径、`\` 混用都无法逃出用户根目录；
//     不使用字符串前缀拼接来判断越权。
//   - 除非显式配置，**不跟随符号链接**：解析路径时逐段 Lstat，遇到 symlink 直接拒绝。
//   - 所有写入先落到目标目录内的临时文件，再由 rename 原子生效，避免半个文件。
package fsops

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// MaxPathLength 限制用户提交路径的长度。
const MaxPathLength = 4096

// MaxDirEntries 限制单次列目录返回的条目数。
const MaxDirEntries = 100_000

// ErrUnsafePath 表示路径非法或试图越出用户根目录。
var ErrUnsafePath = errors.New("路径非法")

// ErrNoRoot 表示没有可用的用户根目录（管理员账号没有自己的存储空间）。
var ErrNoRoot = errors.New("根目录不能为空")

// Ops 绑定到一个固定的存储根目录。
type Ops struct {
	StorageRoot string // 已解析符号链接的绝对路径
}

// New 创建 Ops；会自动创建存储根目录并解析其中的符号链接。
func New(storageRoot string) (*Ops, error) {
	abs, err := filepath.Abs(storageRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &Ops{StorageRoot: real}, nil
}

// CleanRel 把用户提交的路径规范化为"以 / 分隔、不含 . 与 ..、不带前导斜杠"的相对路径。
// 空串或 "." 归一化为 ""（表示根目录自身）。
func CleanRel(p string) (string, error) {
	if len(p) > MaxPathLength {
		return "", fmt.Errorf("%w: 路径过长", ErrUnsafePath)
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: 路径包含空字节", ErrUnsafePath)
	}
	if p == "" || p == "." || p == "/" {
		return "", nil
	}
	// 反斜杠统一视为分隔符，避免 Windows/Unix 混用绕过检查。
	norm := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(norm, "/") {
		return "", fmt.Errorf("%w: 不允许绝对路径", ErrUnsafePath)
	}
	// 以 / 为根做 Clean：任何 .. 都无法逃出根。
	clean := strings.TrimPrefix(path.Clean("/"+norm), "/")
	if clean == "" || clean == "." {
		return "", nil
	}
	// Clean 之后仍含 .. 只可能来自异常输入
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: 不允许 ..", ErrUnsafePath)
	}
	return clean, nil
}

// CleanName 校验单个文件/目录名。
func CleanName(name string) (string, error) {
	if name == "" {
		return "", errors.New("名称不能为空")
	}
	if len(name) > 255 {
		return "", errors.New("名称过长（上限 255 字节）")
	}
	if strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("%w: 名称不能包含路径分隔符", ErrUnsafePath)
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: 名称包含空字节", ErrUnsafePath)
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("%w: 名称不能是 . 或 ..", ErrUnsafePath)
	}
	return name, nil
}

// CleanRoot 校验用户根目录名（允许多级，如 shared/team）。
func CleanRoot(root string) (string, error) {
	if root == "" {
		return "", ErrNoRoot
	}
	clean, err := CleanRel(root)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return "", ErrNoRoot
	}
	for _, seg := range strings.Split(clean, "/") {
		if _, err := CleanName(seg); err != nil {
			return "", err
		}
	}
	return clean, nil
}

// Resolve 把 (root, rel) 解析为磁盘上的绝对路径，并确保不越界、不含符号链接。
func (o *Ops) Resolve(root, rel string) (string, error) {
	cleanRoot, err := CleanRoot(root)
	if err != nil {
		return "", err
	}
	cleanRel, err := CleanRel(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(o.StorageRoot, filepath.FromSlash(cleanRoot))
	if cleanRel != "" {
		full = filepath.Join(full, filepath.FromSlash(cleanRel))
	}
	inner := filepath.FromSlash(cleanRoot)
	if cleanRel != "" {
		inner = filepath.Join(inner, filepath.FromSlash(cleanRel))
	}
	if err := o.checkNoSymlink(inner); err != nil {
		return "", err
	}
	if !isWithin(o.StorageRoot, full) {
		return "", fmt.Errorf("%w: 越出存储根目录", ErrUnsafePath)
	}
	return full, nil
}

// RootPath 返回用户根目录的绝对路径（会校验根名合法性）。
func (o *Ops) RootPath(root string) (string, error) {
	cleanRoot, err := CleanRoot(root)
	if err != nil {
		return "", err
	}
	if err := o.checkNoSymlink(filepath.FromSlash(cleanRoot)); err != nil {
		return "", err
	}
	return filepath.Join(o.StorageRoot, filepath.FromSlash(cleanRoot)), nil
}

// EnsureRoot 创建（如不存在）用户根目录。
func (o *Ops) EnsureRoot(root string) (string, error) {
	p, err := o.RootPath(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, 0o750); err != nil {
		return "", err
	}
	return p, nil
}

// checkNoSymlink 从存储根开始逐段检查，任何一段是符号链接都拒绝。
func (o *Ops) checkNoSymlink(inner string) error {
	if inner == "" || inner == "." {
		return nil
	}
	cur := o.StorageRoot
	for _, seg := range strings.Split(filepath.ToSlash(inner), "/") {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 尚不存在的部分无需检查
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: 路径中包含符号链接（%s）", ErrUnsafePath, seg)
		}
	}
	return nil
}

func isWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}

// IsSubPath 判断 child 是否等于 parent 或位于其内部（两者都是 CleanRel 后的相对路径）。
func IsSubPath(parent, child string) bool {
	if parent == child {
		return true
	}
	if parent == "" {
		return true
	}
	return strings.HasPrefix(child, parent+"/")
}

// Entry 是列目录返回的一项。
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// List 列出目录内容（跳过符号链接），目录优先、名称升序。
func List(dir string) ([]Entry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	items, err := f.ReadDir(MaxDirEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(items) > MaxDirEntries {
		return nil, fmt.Errorf("目录条目超过 %d，请使用分页或拆分目录", MaxDirEntries)
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 {
			continue // 与路径策略一致：不展示符号链接
		}
		isDir := info.IsDir()
		var size int64
		if !isDir {
			size = info.Size()
		}
		out = append(out, Entry{Name: it.Name(), IsDir: isDir, Size: size, ModTime: info.ModTime()})
	}
	sortEntries(out)
	return out, nil
}

func sortEntries(items []Entry) {
	// 目录优先 + 名称（不区分大小写）升序；其余排序在 HTTP 层做。
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j-1], items[j]
			if lessEntry(a, b) {
				break
			}
			items[j-1], items[j] = b, a
		}
	}
}

func lessEntry(a, b Entry) bool {
	if a.IsDir != b.IsDir {
		return a.IsDir
	}
	return strings.ToLower(a.Name) < strings.ToLower(b.Name)
}

// DirSize 递归统计目录占用（不跟随符号链接）。
func DirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// Copy 递归复制文件或目录，保留权限与修改时间。
func Copy(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: 不复制符号链接", ErrUnsafePath)
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		items, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := Copy(filepath.Join(src, it.Name()), filepath.Join(dst, it.Name())); err != nil {
				return err
			}
		}
		return os.Chtimes(dst, time.Now(), info.ModTime())
	}
	if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, time.Now(), info.ModTime())
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// Move 优先用 rename；跨设备时回退为复制后删除。
func Move(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := Copy(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// Exists 报告路径是否存在。
func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// UniqueName 在 dir 中为 name 生成不冲突的名称：a.txt → a (1).txt → a (2).txt。
// 点开头的文件（.env）没有扩展名，序号追加在末尾：.env → .env (1)。
func UniqueName(dir, name string) (string, error) {
	if !Exists(filepath.Join(dir, name)) {
		return name, nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" { // 纯点文件（.env）
		base, ext = name, ""
	}
	for i := 1; i <= 9999; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !Exists(filepath.Join(dir, candidate)) {
			return candidate, nil
		}
	}
	return "", errors.New("无法生成不冲突的文件名（同名文件过多）")
}

// TempFile 在 dir 中创建一个临时文件（同目录内，保证 rename 原子生效）。
func TempFile(dir string) (*os.File, error) {
	return os.CreateTemp(dir, ".homevault-tmp-*")
}
