// Package core 是 homevault 的业务核心：把元数据（state）、文件系统（fsops）、
// 审计日志（audit）与鉴权（auth）串起来，供 HTTP 层调用。
//
// HTTP 层只负责解析参数、映射错误码与写响应；所有涉及"权限 + 配额 + 审计"的
// 逻辑都在这里，保证不会出现"某个接口忘了检查配额"这类遗漏
// （同类项目里 upload 完全没有容量校验，正是这种遗漏）。
package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"homevault/internal/audit"
	"homevault/internal/auth"
	"homevault/internal/config"
	"homevault/internal/fsops"
	"homevault/internal/state"
)

// 业务错误。HTTP 层据此映射状态码。
var (
	ErrNotFound    = errors.New("不存在")
	ErrExists      = errors.New("同名条目已存在")
	ErrForbidden   = errors.New("无权限")
	ErrQuota       = errors.New("超出配额")
	ErrTooLarge    = errors.New("超出单文件大小上限")
	ErrBadRequest  = errors.New("请求参数非法")
	ErrUploadState = errors.New("上传会话状态不符")
	// ErrConflict 表示目标在客户端操作期间已被改动（乐观并发冲突）。
	ErrConflict = errors.New("内容已被其他改动覆盖")
	// ErrNoStorage 表示当前账号没有自己的存储空间（管理员就是这种情况）。
	ErrNoStorage = errors.New("管理员没有自己的存储空间")
	// ErrSpace 表示整个空间目录已经写满（所有用户共享的磁盘上限）。
	ErrSpace = errors.New("空间已满")
)

// Failure 描述一次批量操作中的单项失败。
type Failure struct {
	ID    string `json:"id,omitempty"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

// Actor 是本次操作的发起者（用于审计）。
type Actor struct {
	Username string
	// ActingAs 仅在管理员代为操作用户文件时填写：记录被操作的账号。
	ActingAs  string
	IP        string
	UserAgent string
}

// Service 是业务门面。
type Service struct {
	Cfg    *config.Config
	State  *state.Store
	FS     *fsops.Ops
	Logs   *audit.Logger
	Tokens *auth.TokenManager

	usageMu    sync.Mutex
	usageCache map[string]usageSnapshot
	startedAt  time.Time
}

type usageSnapshot struct {
	used    int64
	fetched time.Time
}

// SpaceUsage 返回整个空间目录的已用字节：所有用户根 + 回收站 + 未提交分片。
func (s *Service) SpaceUsage() (int64, error) {
	var total int64
	for _, u := range s.State.Users() {
		if u == nil || strings.TrimSpace(u.Root) == "" {
			continue
		}
		root, err := s.FS.RootPath(u.Root)
		if err != nil {
			continue
		}
		size, err := fsops.DirSize(root)
		if err != nil {
			continue
		}
		total += size + s.State.TrashUsedByRoot(u.Root)
	}
	for _, sess := range s.State.UploadSessions() {
		if size, err := fsops.DirSize(filepath.Join(s.Cfg.UploadPath(), sess.ID)); err == nil {
			total += size
		}
	}
	return total, nil
}

// CheckSpace 校验再写入 add 字节是否超过空间总容量（SpaceQuota = 0 表示不限）。
// 这是"整个空间目录最多占多少磁盘"的硬上限，与单用户配额是两回事。
func (s *Service) CheckSpace(add int64) error {
	st := s.Settings()
	if st.SpaceQuota == 0 {
		return nil
	}
	used, err := s.SpaceUsage()
	if err != nil {
		return err
	}
	if used+add > int64(st.SpaceQuota) {
		return fmt.Errorf("%w: 空间已用 %s / 上限 %s，本次还要写入 %s",
			ErrSpace, humanSize(used), humanSize(int64(st.SpaceQuota)), humanSize(add))
	}
	return nil
}

// New 构造 Service。
func New(cfg *config.Config, st *state.Store, fs *fsops.Ops, logs *audit.Logger, tokens *auth.TokenManager) *Service {
	return &Service{
		Cfg:        cfg,
		State:      st,
		FS:         fs,
		Logs:       logs,
		Tokens:     tokens,
		usageCache: map[string]usageSnapshot{},
		startedAt:  time.Now(),
	}
}

// StartedAt 返回进程启动时间。
func (s *Service) StartedAt() time.Time { return s.startedAt }

// Settings 返回当前运行期设置。
func (s *Service) Settings() state.Settings { return s.State.Settings() }

// audit 记录审计日志；失败只返回错误，绝不影响主流程。
func (s *Service) audit(act Actor, action, target, status string, bytes int64, message string) {
	_ = s.Logs.Log(audit.Entry{
		User: act.Username, ActingAs: act.ActingAs, IP: act.IP, UserAgent: act.UserAgent,
		Action: action, Target: target, Status: status, Bytes: bytes, Message: message,
	})
}

// Audit 供 HTTP 层直接写审计（如登录、用户管理）。
func (s *Service) Audit(act Actor, action, target, status string, bytes int64, message string) {
	s.audit(act, action, target, status, bytes, message)
}

func newID(prefix string) string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buf)
}

// ---------------------------------------------------------------------------
// 用量与配额
// ---------------------------------------------------------------------------

// RootAbs 返回用户根目录的绝对路径（不存在时创建）。
// 管理员没有自己的存储空间，因此这里会直接拒绝。
func (s *Service) RootAbs(u *state.User) (string, error) {
	if u == nil {
		return "", fmt.Errorf("%w: 用户不存在", ErrForbidden)
	}
	if strings.TrimSpace(u.Root) == "" {
		return "", fmt.Errorf("%w：请在管理页面选择目标用户后再操作", ErrNoStorage)
	}
	return s.FS.EnsureRoot(u.Root)
}

// Usage 返回该用户已用字节数与回收站占用。
// 目录大小按用户根缓存 5 秒，避免每次请求都遍历整棵树；写操作会主动失效缓存。
func (s *Service) Usage(u *state.User) (int64, int64, error) {
	return s.usage(u, false)
}

// usage 统计用量。force 为真时忽略 5 秒缓存，直接重新遍历
// （配额校验必须用实时数据，否则并发写可以突破配额）。
// 返回值 used 已包含该账号"正在上传但尚未提交"的分片占用。
func (s *Service) usage(u *state.User, force bool) (int64, int64, error) {
	root, err := s.RootAbs(u)
	if err != nil {
		return 0, 0, err
	}
	var used int64
	s.usageMu.Lock()
	snap, ok := s.usageCache[u.Root]
	s.usageMu.Unlock()

	if !force && ok && time.Since(snap.fetched) < 5*time.Second {
		used = snap.used
	} else {
		used, err = fsops.DirSize(root)
		if err != nil {
			return 0, 0, err
		}
		s.usageMu.Lock()
		s.usageCache[u.Root] = usageSnapshot{used: used, fetched: time.Now()}
		s.usageMu.Unlock()
	}
	return used + s.PendingUploadBytes(u.Username), s.State.TrashUsedByRoot(u.Root), nil
}

// PendingUploadBytes 返回某账号尚未提交的分片总占用。
// 这些字节同样占磁盘，必须计入配额，否则可以靠"只传不提交"把磁盘放大数倍。
func (s *Service) PendingUploadBytes(username string) int64 {
	if username == "" {
		return 0
	}
	var total int64
	for _, sess := range s.State.UploadSessions() {
		if sess.Username != username {
			continue
		}
		if size, err := fsops.DirSize(filepath.Join(s.Cfg.UploadPath(), sess.ID)); err == nil {
			total += size
		}
	}
	return total
}

// InvalidateUsage 让某个根目录的用量缓存失效。
func (s *Service) InvalidateUsage(root string) {
	s.usageMu.Lock()
	delete(s.usageCache, root)
	s.usageMu.Unlock()
}

// CheckQuota 校验再写入 add 字节是否超配额（quota = 0 表示不限）。
// 这里强制使用实时用量，避免缓存窗口内的并发写入突破配额。
// 注意 used 已包含"已收分片但尚未提交"的占用。
func (s *Service) CheckQuota(u *state.User, add int64) error {
	if u.Quota == 0 {
		return nil
	}
	used, trash, err := s.usage(u, true)
	if err != nil {
		return err
	}
	if used+trash+add > u.Quota {
		return fmt.Errorf("%w: 已用 %s（含未完成上传）+ 回收站 %s，再加 %s 会超过上限 %s",
			ErrQuota, humanSize(used), humanSize(trash), humanSize(add), humanSize(u.Quota))
	}
	return nil
}

// CheckQuotaFinalize 用于"把待提交分片落成正式文件"这一步：
// 分片字节已经计入 used，落盘只是把它们从 uploads/ 搬到 files/，
// 因此这里只按根目录真实占用 + 回收站 + 最终大小来判断，避免重复计算。
func (s *Service) CheckQuotaFinalize(u *state.User, size int64) error {
	if u.Quota == 0 {
		return nil
	}
	root, err := s.RootAbs(u)
	if err != nil {
		return err
	}
	used, err := fsops.DirSize(root)
	if err != nil {
		return err
	}
	trash := s.State.TrashUsedByRoot(u.Root)
	if used+trash+size > u.Quota {
		return fmt.Errorf("%w: 已用 %s + 回收站 %s，再落盘 %s 会超过上限 %s",
			ErrQuota, humanSize(used), humanSize(trash), humanSize(size), humanSize(u.Quota))
	}
	return nil
}

func humanSize(n int64) string { return state.FormatSize(n) }

// ---------------------------------------------------------------------------
// 路径解析
// ---------------------------------------------------------------------------

// Resolve 把用户根内的相对路径解析为绝对路径（含安全检查）。
// 管理员没有自己的存储空间，因此这里同样会拒绝空根。
func (s *Service) Resolve(u *state.User, rel string) (string, error) {
	if u == nil {
		return "", fmt.Errorf("%w: 用户不存在", ErrForbidden)
	}
	if strings.TrimSpace(u.Root) == "" {
		return "", fmt.Errorf("%w：请在管理页面选择目标用户后再操作", ErrNoStorage)
	}
	clean, err := fsops.CleanRel(rel)
	if err != nil {
		return "", err
	}
	return s.FS.Resolve(u.Root, clean)
}

// List 列出目录；返回绝对路径与条目。
func (s *Service) List(u *state.User, rel string) (string, []fsops.Entry, error) {
	abs, err := s.Resolve(u, rel)
	if err != nil {
		return "", nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("%w: 目录不存在", ErrNotFound)
		}
		return "", nil, err
	}
	if !fi.IsDir() {
		return "", nil, fmt.Errorf("%w: 不是目录", ErrBadRequest)
	}
	entries, err := fsops.List(abs)
	if err != nil {
		return "", nil, err
	}
	return abs, entries, nil
}

// ---------------------------------------------------------------------------
// 写操作
// ---------------------------------------------------------------------------

// Mkdir 新建目录，返回新目录的相对路径。
func (s *Service) Mkdir(act Actor, u *state.User, rel, name string) (string, error) {
	cleanName, err := fsops.CleanName(name)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	parent, err := s.Resolve(u, rel)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: 父目录不存在", ErrNotFound)
	}
	target := filepath.Join(parent, cleanName)
	if fsops.Exists(target) {
		return "", fmt.Errorf("%w: %s", ErrExists, cleanName)
	}
	if err := os.Mkdir(target, 0o750); err != nil {
		return "", err
	}
	relOut := joinRel(rel, cleanName)
	s.audit(act, audit.ActionMkdir, relOut, audit.StatusOK, 0, "")
	s.InvalidateUsage(u.Root)
	return relOut, nil
}

// Rename 重命名（同目录内），返回新的相对路径。
func (s *Service) Rename(act Actor, u *state.User, rel, newName string) (string, error) {
	cleanName, err := fsops.CleanName(newName)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	clean, err := fsops.CleanRel(rel)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return "", fmt.Errorf("%w: 不能重命名根目录", ErrBadRequest)
	}
	src, err := s.FS.Resolve(u.Root, clean)
	if err != nil {
		return "", err
	}
	if !fsops.Exists(src) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, clean)
	}
	parent := parentRel(clean)
	destRel := joinRel(parent, cleanName)
	if destRel == clean {
		return clean, nil // 名称未变，幂等
	}
	dest, err := s.FS.Resolve(u.Root, destRel)
	if err != nil {
		return "", err
	}
	if fsops.Exists(dest) {
		return "", fmt.Errorf("%w: %s", ErrExists, cleanName)
	}
	if err := fsops.Move(src, dest); err != nil {
		return "", err
	}
	s.audit(act, audit.ActionRename, clean+" → "+destRel, audit.StatusOK, 0, "")
	s.InvalidateUsage(u.Root)
	return destRel, nil
}

// Move 把多个条目移动到 dest 目录。
func (s *Service) Move(act Actor, u *state.User, paths []string, dest string) ([]string, error) {
	return s.transfer(act, u, paths, dest, false)
}

// Copy 把多个条目复制到 dest 目录。
func (s *Service) Copy(act Actor, u *state.User, paths []string, dest string) ([]string, error) {
	return s.transfer(act, u, paths, dest, true)
}

func (s *Service) transfer(act Actor, u *state.User, paths []string, dest string, copying bool) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: 未选择任何条目", ErrBadRequest)
	}
	if len(paths) > 500 {
		return nil, fmt.Errorf("%w: 单次最多操作 500 个条目", ErrBadRequest)
	}
	destClean, err := fsops.CleanRel(dest)
	if err != nil {
		return nil, err
	}
	destAbs, err := s.FS.Resolve(u.Root, destClean)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(destAbs)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: 目标目录不存在", ErrNotFound)
	}

	out := make([]string, 0, len(paths))
	for _, p := range paths {
		srcClean, err := fsops.CleanRel(p)
		if err != nil {
			return nil, err
		}
		if srcClean == "" {
			return nil, fmt.Errorf("%w: 不能操作根目录", ErrBadRequest)
		}
		if fsops.IsSubPath(srcClean, destClean) {
			return nil, fmt.Errorf("%w: 不能把 %s 移动到它自身或其子目录中", ErrBadRequest, srcClean)
		}
		srcAbs, err := s.FS.Resolve(u.Root, srcClean)
		if err != nil {
			return nil, err
		}
		if !fsops.Exists(srcAbs) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, srcClean)
		}
		base := path.Base(srcClean)
		var targetName string
		if copying {
			targetName, err = copyName(destAbs, base)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(srcAbs)
			if err != nil {
				return nil, err
			}
			var add int64
			if info.IsDir() {
				add, err = fsops.DirSize(srcAbs)
				if err != nil {
					return nil, err
				}
			} else {
				add = info.Size()
			}
			if err := s.CheckQuota(u, add); err != nil {
				return nil, err
			}
			if err := s.CheckSpace(add); err != nil {
				return nil, err
			}
		} else {
			targetName = base
			if fsops.Exists(filepath.Join(destAbs, targetName)) {
				return nil, fmt.Errorf("%w: 目标目录已有 %s", ErrExists, targetName)
			}
		}
		targetAbs := filepath.Join(destAbs, targetName)
		if copying {
			if err := fsops.Copy(srcAbs, targetAbs); err != nil {
				return nil, err
			}
		} else {
			if err := fsops.Move(srcAbs, targetAbs); err != nil {
				return nil, err
			}
		}
		outRel := joinRel(destClean, targetName)
		out = append(out, outRel)
		action := audit.ActionMove
		if copying {
			action = audit.ActionCopy
		}
		s.audit(act, action, srcClean+" → "+outRel, audit.StatusOK, 0, "")
	}
	s.InvalidateUsage(u.Root)
	return out, nil
}

// copyName 为 a.pdf 生成 "a - 副本.pdf"（冲突时继续加序号）。
func copyName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := base + " - 副本" + ext
	return fsops.UniqueName(dir, candidate)
}

// ---------------------------------------------------------------------------
// 回收站
// ---------------------------------------------------------------------------

// Trash 把条目移入回收站。
func (s *Service) Trash(act Actor, u *state.User, paths []string) ([]*state.TrashEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: 未选择任何条目", ErrBadRequest)
	}
	if len(paths) > 500 {
		return nil, fmt.Errorf("%w: 单次最多删除 500 个条目", ErrBadRequest)
	}
	// 先算出总量再一次性校验配额，避免部分成功、部分失败。
	type plan struct {
		srcRel string
		srcAbs string
		size   int64
		isDir  bool
	}
	plans := make([]plan, 0, len(paths))
	var total int64
	for _, p := range paths {
		clean, err := fsops.CleanRel(p)
		if err != nil {
			return nil, err
		}
		if clean == "" {
			return nil, fmt.Errorf("%w: 不能删除根目录", ErrBadRequest)
		}
		abs, err := s.FS.Resolve(u.Root, clean)
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w: %s", ErrNotFound, clean)
			}
			return nil, err
		}
		var size int64
		if fi.IsDir() {
			size, err = fsops.DirSize(abs)
			if err != nil {
				return nil, err
			}
		} else {
			size = fi.Size()
		}
		total += size
		plans = append(plans, plan{srcRel: clean, srcAbs: abs, size: size, isDir: fi.IsDir()})
	}
	// 删除不需要配额校验：文件只是从用户根挪进回收站，
	// 已用+回收站的总额不变，配额已满时也必须能删（否则用户会被自己的数据卡死）。
	_ = total

	retention := s.Settings().TrashDur()
	var expires *time.Time
	if retention > 0 {
		t := time.Now().UTC().Add(retention)
		expires = &t
	}
	entries := make([]*state.TrashEntry, 0, len(plans))
	for _, pl := range plans {
		id := newID("t_")
		entryDir := filepath.Join(s.Cfg.TrashPath(), id)
		if err := os.MkdirAll(entryDir, 0o750); err != nil {
			return nil, err
		}
		payload := filepath.Join(entryDir, "payload")
		if err := fsops.Move(pl.srcAbs, payload); err != nil {
			_ = os.RemoveAll(entryDir)
			return nil, err
		}
		entry := &state.TrashEntry{
			ID: id, Name: path.Base(pl.srcRel), OriginalPath: pl.srcRel, Root: u.Root,
			Size: pl.size, IsDir: pl.isDir, DeletedBy: act.Username,
			DeletedAt: time.Now().UTC(), ExpiresAt: expires,
		}
		if err := s.State.PutTrashEntry(entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
		s.audit(act, audit.ActionDelete, pl.srcRel, audit.StatusOK, pl.size, "")
	}
	s.InvalidateUsage(u.Root)
	return entries, nil
}

// VisibleTrash 返回调用者可见的回收站条目。
// 管理员"不带 ?user="时看全部；一旦代操作某个账号，就只看那个账号的条目，
// 否则在 bob 的视图里会看到 alice 的文件名。
func (s *Service) VisibleTrash(u *state.User) []*state.TrashEntry {
	all := s.State.TrashEntries()
	if u.IsAdmin && strings.TrimSpace(u.Root) == "" {
		return all
	}
	out := make([]*state.TrashEntry, 0, len(all))
	for _, e := range all {
		if e.Root == u.Root {
			out = append(out, e)
		}
	}
	return out
}

func (s *Service) canTouchTrash(u *state.User, e *state.TrashEntry) bool {
	// 管理员只有"不带目标账号"时才能动任意条目；代操作时仍受该账号根目录约束。
	if u.IsAdmin && strings.TrimSpace(u.Root) == "" {
		return true
	}
	return e.Root == u.Root
}

// Restore 恢复回收站条目。
func (s *Service) Restore(act Actor, u *state.User, ids []string) ([]string, []Failure, error) {
	restored := make([]string, 0, len(ids))
	failed := make([]Failure, 0)
	for _, id := range ids {
		entry, ok := s.State.TrashEntry(id)
		if !ok {
			failed = append(failed, Failure{ID: id, Error: "记录不存在"})
			continue
		}
		if !s.canTouchTrash(u, entry) {
			failed = append(failed, Failure{ID: id, Error: "无权操作该回收站条目"})
			continue
		}
		if _, err := s.FS.EnsureRoot(entry.Root); err != nil {
			failed = append(failed, Failure{ID: id, Error: err.Error()})
			continue
		}
		target, err := s.FS.Resolve(entry.Root, entry.OriginalPath)
		if err != nil {
			failed = append(failed, Failure{ID: id, Error: err.Error()})
			continue
		}
		if fsops.Exists(target) {
			failed = append(failed, Failure{ID: id, Error: "原位置已有同名条目"})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			failed = append(failed, Failure{ID: id, Error: err.Error()})
			continue
		}
		payload := filepath.Join(s.Cfg.TrashPath(), entry.ID, "payload")
		if err := fsops.Move(payload, target); err != nil {
			failed = append(failed, Failure{ID: id, Error: err.Error()})
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.Cfg.TrashPath(), entry.ID))
		if err := s.State.DeleteTrashEntry(id); err != nil {
			failed = append(failed, Failure{ID: id, Error: err.Error()})
			continue
		}
		restored = append(restored, entry.OriginalPath)
		s.audit(act, audit.ActionRestore, entry.OriginalPath, audit.StatusOK, entry.Size, "")
		s.InvalidateUsage(entry.Root)
	}
	return restored, failed, nil
}

// Purge 永久删除回收站条目。
func (s *Service) Purge(act Actor, u *state.User, ids []string) ([]string, error) {
	deleted := make([]string, 0, len(ids))
	for _, id := range ids {
		entry, ok := s.State.TrashEntry(id)
		if !ok {
			return deleted, fmt.Errorf("%w: 记录 %s", ErrNotFound, id)
		}
		if !s.canTouchTrash(u, entry) {
			return deleted, fmt.Errorf("%w: 无权操作该回收站条目", ErrForbidden)
		}
		if err := os.RemoveAll(filepath.Join(s.Cfg.TrashPath(), entry.ID)); err != nil {
			return deleted, err
		}
		if err := s.State.DeleteTrashEntry(id); err != nil {
			return deleted, err
		}
		deleted = append(deleted, id)
		s.audit(act, audit.ActionPurge, entry.OriginalPath, audit.StatusOK, entry.Size, "")
	}
	return deleted, nil
}

// EmptyTrash 清空调用者可见的全部回收站条目。
func (s *Service) EmptyTrash(act Actor, u *state.User) (int, int64, error) {
	entries := s.VisibleTrash(u)
	var count int
	var bytes int64
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.Cfg.TrashPath(), e.ID)); err != nil {
			return count, bytes, err
		}
		if err := s.State.DeleteTrashEntry(e.ID); err != nil {
			return count, bytes, err
		}
		count++
		bytes += e.Size
	}
	s.audit(act, audit.ActionTrashEmpty, "", audit.StatusOK, bytes, fmt.Sprintf("清空 %d 项", count))
	return count, bytes, nil
}

// PurgeExpiredTrash 清理过期条目，返回条数与字节数（供后台任务调用）。
func (s *Service) PurgeExpiredTrash() (int, int64, error) {
	now := time.Now().UTC()
	all := s.State.TrashEntries()
	var count int
	var bytes int64
	for _, e := range all {
		if e.ExpiresAt == nil || e.ExpiresAt.After(now) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.Cfg.TrashPath(), e.ID)); err != nil {
			return count, bytes, err
		}
		if err := s.State.DeleteTrashEntry(e.ID); err != nil {
			return count, bytes, err
		}
		count++
		bytes += e.Size
	}
	if count > 0 {
		s.audit(Actor{Username: "system"}, audit.ActionCleanup, "trash", audit.StatusOK, bytes,
			fmt.Sprintf("回收站保留期到期，永久删除 %d 项（%s）", count, humanSize(bytes)))
	}
	return count, bytes, nil
}

// ---------------------------------------------------------------------------
// 分片上传
// ---------------------------------------------------------------------------

// InitUpload 创建（或复用）分片上传会话。
func (s *Service) InitUpload(act Actor, u *state.User, dir, name string, size, chunkSize int64) (*state.UploadSession, error) {
	cleanName, err := fsops.CleanName(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	cleanDir, err := fsops.CleanRel(dir)
	if err != nil {
		return nil, err
	}
	dirAbs, err := s.FS.Resolve(u.Root, cleanDir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dirAbs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: 目标目录不存在", ErrNotFound)
	}
	if size < 0 {
		return nil, fmt.Errorf("%w: size 不能为负", ErrBadRequest)
	}
	st := s.Settings()
	if st.MaxUploadSize > 0 && size > int64(st.MaxUploadSize) {
		return nil, fmt.Errorf("%w: 单文件上限 %s", ErrTooLarge, humanSize(int64(st.MaxUploadSize)))
	}
	if existing, ok := s.State.FindResumable(u.Username, cleanDir, cleanName, size); ok {
		// 续传：已收分片本来就算在 used 里，这里再校验一次 size 会重复计入。
		return existing, nil
	}
	// 只有新建会话才做整体预检（此后每个分片写入还会再校验一次）。
	if err := s.CheckQuota(u, size); err != nil {
		return nil, err
	}
	if err := s.CheckSpace(size); err != nil {
		return nil, err
	}
	chunk := st.ChunkSize.Bytes()
	if chunkSize > 0 {
		chunk = chunkSize
	}
	if chunk < 64<<10 {
		chunk = 64 << 10
	}
	if size > 0 && size < chunk {
		chunk = size
	}
	chunkCount := 1
	if size > 0 {
		chunkCount = int((size + chunk - 1) / chunk)
	}
	sess := &state.UploadSession{
		ID: newID("u_"), Username: u.Username, Dir: cleanDir, Name: cleanName,
		Size: size, ChunkSize: chunk, ChunkCount: chunkCount,
		Received: []int{}, CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(st.UploadSessionDur()),
	}
	if err := os.MkdirAll(filepath.Join(s.Cfg.UploadPath(), sess.ID), 0o750); err != nil {
		return nil, err
	}
	if err := s.State.PutUploadSession(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// UploadSession 取会话（校验归属）。
// 注意：管理员"不带 ?user= 目标用户"时没有自己的存储空间，
// 因此也不能碰别人的上传会话——否则可以静默丢弃他人正在进行的上传。
func (s *Service) UploadSession(u *state.User, id string) (*state.UploadSession, error) {
	sess, ok := s.State.UploadSession(id)
	if !ok {
		return nil, fmt.Errorf("%w: 上传会话不存在或已过期", ErrNotFound)
	}
	if u == nil || strings.TrimSpace(u.Root) == "" {
		return nil, fmt.Errorf("%w：请在管理页面选择目标用户后再操作", ErrNoStorage)
	}
	if sess.Username != u.Username {
		return nil, fmt.Errorf("%w: 该上传会话不属于你", ErrForbidden)
	}
	return sess, nil
}

// WriteChunk 写入一个分片；同一 index 重复写入是幂等的。
func (s *Service) WriteChunk(u *state.User, id string, index int, r io.Reader, length int64, shaHex string) (*state.UploadSession, error) {
	sess, err := s.UploadSession(u, id)
	if err != nil {
		return nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, fmt.Errorf("%w: 上传会话已过期", ErrUploadState)
	}
	want, err := sess.ChunkLength(index)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if length != want {
		return nil, fmt.Errorf("%w: 分片 %d 长度应为 %d 字节，实际 %d", ErrBadRequest, index, want, length)
	}
	// 重传同一个分片是幂等的，不再重复计入；新分片必须先过配额，
	// 否则可以"只传不提交"把磁盘堆到配额的数倍。
	received := false
	for _, idx := range sess.Received {
		if idx == index {
			received = true
			break
		}
	}
	if !received {
		if err := s.CheckQuota(u, want); err != nil {
			return nil, err
		}
		if err := s.CheckSpace(want); err != nil {
			return nil, err
		}
	}
	sessDir := filepath.Join(s.Cfg.UploadPath(), sess.ID)
	if err := os.MkdirAll(sessDir, 0o750); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(sessDir, ".chunk-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), r)
	if err != nil {
		tmp.Close()
		return nil, err
	}
	if written != want {
		tmp.Close()
		return nil, fmt.Errorf("%w: 分片 %d 实际收到 %d 字节，应为 %d", ErrBadRequest, index, written, want)
	}
	if shaHex != "" && !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), shaHex) {
		tmp.Close()
		return nil, fmt.Errorf("%w: 分片 %d SHA-256 校验失败", ErrBadRequest, index)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	final := filepath.Join(sessDir, fmt.Sprintf("chunk-%d", index))
	if err := os.Rename(tmpName, final); err != nil {
		return nil, err
	}
	return s.State.MarkChunkReceived(id, index)
}

// CompleteUpload 合并分片并落盘，返回最终文件名与相对路径。
func (s *Service) CompleteUpload(act Actor, u *state.User, id string) (string, string, int64, error) {
	sess, err := s.UploadSession(u, id)
	if err != nil {
		return "", "", 0, err
	}
	if !sess.Complete() {
		return "", "", 0, fmt.Errorf("%w: 仍有 %d 个分片未上传", ErrUploadState, sess.ChunkCount-len(sess.Received))
	}
	if time.Now().After(sess.ExpiresAt) {
		return "", "", 0, fmt.Errorf("%w: 上传会话已过期", ErrUploadState)
	}
	if err := s.CheckQuotaFinalize(u, sess.Size); err != nil {
		return "", "", 0, err
	}
	dirAbs, err := s.FS.Resolve(u.Root, sess.Dir)
	if err != nil {
		return "", "", 0, err
	}
	if fi, err := os.Stat(dirAbs); err != nil || !fi.IsDir() {
		return "", "", 0, fmt.Errorf("%w: 目标目录已不存在", ErrNotFound)
	}
	sessDir := filepath.Join(s.Cfg.UploadPath(), sess.ID)

	tmp, err := fsops.TempFile(dirAbs)
	if err != nil {
		return "", "", 0, err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	var total int64
	for i := 0; i < sess.ChunkCount; i++ {
		chunkPath := filepath.Join(sessDir, fmt.Sprintf("chunk-%d", i))
		f, err := os.Open(chunkPath)
		if err != nil {
			cleanup()
			return "", "", 0, fmt.Errorf("%w: 分片 %d 缺失", ErrUploadState, i)
		}
		n, err := io.Copy(tmp, f)
		f.Close()
		if err != nil {
			cleanup()
			return "", "", 0, err
		}
		total += n
	}
	if total != sess.Size {
		cleanup()
		return "", "", 0, fmt.Errorf("%w: 合并后大小 %d 与声明 %d 不符", ErrUploadState, total, sess.Size)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", "", 0, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", "", 0, err
	}

	finalName, err := fsops.UniqueName(dirAbs, sess.Name)
	if err != nil {
		os.Remove(tmpName)
		return "", "", 0, err
	}
	finalAbs := filepath.Join(dirAbs, finalName)
	if err := os.Rename(tmpName, finalAbs); err != nil {
		os.Remove(tmpName)
		return "", "", 0, err
	}
	_ = os.Chmod(finalAbs, 0o640)

	_ = os.RemoveAll(sessDir)
	if err := s.State.DeleteUploadSession(sess.ID); err != nil {
		return "", "", 0, err
	}
	relOut := joinRel(sess.Dir, finalName)
	s.audit(act, audit.ActionUpload, relOut, audit.StatusOK, sess.Size, "")
	s.InvalidateUsage(u.Root)
	return finalName, relOut, sess.Size, nil
}

// CancelUpload 取消上传会话并清理临时分片。
func (s *Service) CancelUpload(act Actor, u *state.User, id string) error {
	sess, err := s.UploadSession(u, id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.Cfg.UploadPath(), sess.ID)); err != nil {
		return err
	}
	return s.State.DeleteUploadSession(sess.ID)
}

// CleanupUploadSessions 清理过期会话，返回清理数量。
func (s *Service) CleanupUploadSessions() (int, error) {
	now := time.Now().UTC()
	removed := 0
	for _, sess := range s.State.UploadSessions() {
		if sess.ExpiresAt.After(now) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.Cfg.UploadPath(), sess.ID)); err != nil {
			return removed, err
		}
		if err := s.State.DeleteUploadSession(sess.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// CleanupExpiredUploadsDir 清理磁盘上已无记录的孤儿会话目录。
func (s *Service) CleanupExpiredUploadsDir() error {
	known := map[string]bool{}
	for _, sess := range s.State.UploadSessions() {
		known[sess.ID] = true
	}
	items, err := os.ReadDir(s.Cfg.UploadPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, it := range items {
		if !it.IsDir() || known[it.Name()] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.Cfg.UploadPath(), it.Name()))
	}
	return nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// parentRel 返回相对路径的父目录（无父目录时返回空串）。
func parentRel(rel string) string {
	idx := strings.LastIndex(rel, "/")
	if idx < 0 {
		return ""
	}
	return rel[:idx]
}
