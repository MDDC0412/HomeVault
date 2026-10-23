// Package state 保存 homevault 的全部元数据：用户、回收站索引、分片上传会话。
//
// 存储方式是单个 JSON 文件 + 原子替换（写临时文件后 rename）。
// 这是刻意的取舍：面向"几十个用户、几千个文件"的自托管场景，换来零依赖与可读可备份。
// 代价是每次写入都会重写整个文件，因此不适合上千用户或高频并发写入；
// 若将来需要，替换本包的实现为 SQLite 即可，其余代码只依赖这里的方法签名。
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// usernameRe 是账号名的合法形式。
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

// ValidateUsername 校验账号名。
func ValidateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("账号 %q 不合法：只允许 3-32 位字母、数字、下划线或短横线", name)
	}
	return nil
}

// 权限位。
const (
	PermRead        = "read"
	PermUpload      = "upload"
	PermModify      = "modify"
	PermDelete      = "delete"
	PermTrashView   = "trash_view"
	PermTrashManage = "trash_manage"
	PermUsersManage = "users_manage"
	PermLogsView    = "logs_view"
)

// AllPermissions 列出全部权限位（管理员隐含拥有）。
var AllPermissions = []string{
	PermRead, PermUpload, PermModify, PermDelete,
	PermTrashView, PermTrashManage, PermUsersManage, PermLogsView,
}

// UserPermissions 是普通用户可以拥有的权限位（管理员隐含拥有全部）。
//
// 回收站与审计日志按"账号自己"的维度隔离：普通账号被授予这些位后，
// 只能看到/操作自己那条根目录下的回收站、只能看自己的操作日志（见 handlers_admin.go
// 的 VisibleTrash 按 e.Root 过滤、handleLogs 对普通账号强制 q.User = 当前账号），
// 因此开放给普通账号是安全的、且正是产品需要的行为。
// users_manage（用户管理）仍只属于管理员：否则普通账号可自造管理员，构成提权。
var UserPermissions = []string{
	PermRead, PermUpload, PermModify, PermDelete,
	PermTrashView, PermTrashManage, PermLogsView,
}

// ValidUserPermission 判断某个权限位是否允许分配给普通用户。
func ValidUserPermission(p string) bool {
	for _, v := range UserPermissions {
		if v == p {
			return true
		}
	}
	return false
}

// ValidPermission 判断权限名是否合法。
func ValidPermission(p string) bool {
	for _, v := range AllPermissions {
		if v == p {
			return true
		}
	}
	return false
}

// User 是一个账号。
type User struct {
	Username      string          `json:"username"`
	DisplayName   string          `json:"displayName,omitempty"`
	PasswordHash  string          `json:"passwordHash"`
	Root          string          `json:"root"`
	Permissions   map[string]bool `json:"permissions"`
	IsAdmin       bool            `json:"isAdmin"`
	Quota         int64           `json:"quota"`         // 0 = 不限
	RateLimitMbps int             `json:"rateLimitMbps"` // 每用户双向限速（Mbps），0 = 不限
	Disabled      bool            `json:"disabled"`
	// MustChangeCredentials 为真时除"改凭据/登出"外的接口一律拒绝（首次启动默认 admin/admin 场景）。
	MustChangeCredentials bool      `json:"mustChangeCredentials,omitempty"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// Validate 校验用户记录的一致性。
//
// 关键规则：**管理员没有自己的存储空间**（Root 必须为空），普通用户必须有存储空间。
// 这样"管理员只能操作别人的文件"在结构上就成立，而不是靠界面约定。
func (u *User) Validate() error {
	if err := ValidateUsername(u.Username); err != nil {
		return err
	}
	if u.IsAdmin {
		if strings.TrimSpace(u.Root) != "" {
			return fmt.Errorf("账号 %q 是管理员，不能拥有存储空间（root 必须为空）", u.Username)
		}
	} else if strings.TrimSpace(u.Root) == "" {
		return fmt.Errorf("账号 %q 必须指定存储空间（root 不能为空）", u.Username)
	}
	if !u.IsAdmin {
		for p := range u.Permissions {
			if !ValidUserPermission(p) {
				return fmt.Errorf("账号 %q 不能拥有管理员专属权限 %q（用户管理只属于管理员）", u.Username, p)
			}
		}
	}
	if u.Quota < 0 {
		return fmt.Errorf("账号 %q 的配额不能为负数", u.Username)
	}
	if u.RateLimitMbps < 0 {
		return fmt.Errorf("账号 %q 的限速不能为负数", u.Username)
	}
	return nil
}

// Can 判断用户是否拥有某权限（管理员恒为真）。
func (u *User) Can(perm string) bool {
	if u == nil || u.Disabled {
		return false
	}
	if u.IsAdmin {
		return true
	}
	return u.Permissions[perm]
}

// PermList 返回排序后的权限列表（用于 API 输出）。
func (u *User) PermList() []string {
	out := make([]string, 0, len(u.Permissions))
	for _, p := range AllPermissions {
		if u.IsAdmin || u.Permissions[p] {
			out = append(out, p)
		}
	}
	return out
}

// Clone 深拷贝，避免调用方拿到内部引用。
func (u *User) Clone() *User {
	if u == nil {
		return nil
	}
	c := *u
	c.Permissions = make(map[string]bool, len(u.Permissions))
	for k, v := range u.Permissions {
		c.Permissions[k] = v
	}
	return &c
}

// TrashEntry 是一条回收站记录。文件实体存放在 DataDir/trash/<ID>/payload。
type TrashEntry struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	OriginalPath string     `json:"originalPath"` // 相对于用户 Root
	Root         string     `json:"root"`         // 归属的用户根目录名
	Size         int64      `json:"size"`
	IsDir        bool       `json:"isDir"`
	DeletedBy    string     `json:"deletedBy"`
	DeletedAt    time.Time  `json:"deletedAt"`
	ExpiresAt    *time.Time `json:"expiresAt"` // nil = 永不自动清理
}

// UploadSession 是一次分片上传会话。
type UploadSession struct {
	ID         string    `json:"id"`
	Username   string    `json:"username"`
	Dir        string    `json:"dir"` // 目标目录（用户根内相对路径）
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ChunkSize  int64     `json:"chunkSize"`
	ChunkCount int       `json:"chunkCount"`
	Received   []int     `json:"received"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// Clone 深拷贝。
func (s *UploadSession) Clone() *UploadSession {
	if s == nil {
		return nil
	}
	c := *s
	c.Received = append([]int(nil), s.Received...)
	return &c
}

// ChunkLength 返回第 index 个分片应有的字节数。
func (s *UploadSession) ChunkLength(index int) (int64, error) {
	if index < 0 || index >= s.ChunkCount {
		return 0, fmt.Errorf("分片下标越界: %d", index)
	}
	if index < s.ChunkCount-1 {
		return s.ChunkSize, nil
	}
	remainder := s.Size - s.ChunkSize*int64(s.ChunkCount-1)
	return remainder, nil
}

// BytesReceived 返回已接收字节数。
func (s *UploadSession) BytesReceived() int64 {
	var total int64
	for _, i := range s.Received {
		n, err := s.ChunkLength(i)
		if err == nil {
			total += n
		}
	}
	return total
}

// Complete 报告是否所有分片都已到齐。
func (s *UploadSession) Complete() bool {
	return len(s.Received) == s.ChunkCount
}

type fileData struct {
	Version  int                       `json:"version"`
	Settings *Settings                 `json:"settings,omitempty"`
	Users    map[string]*User          `json:"users"`
	Trash    map[string]*TrashEntry    `json:"trash"`
	Uploads  map[string]*UploadSession `json:"uploads"`
}

// Store 是元数据存储。所有方法并发安全。
type Store struct {
	mu   sync.RWMutex
	path string
	data fileData
}

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("记录不存在")

// ErrExists 表示记录已存在。
var ErrExists = errors.New("记录已存在")

// Open 打开（或初始化）元数据文件。
func Open(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: fileData{
			Version: 1,
			Users:   map[string]*User{},
			Trash:   map[string]*TrashEntry{},
			Uploads: map[string]*UploadSession{},
		},
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		return s, s.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
		}
	}
	if s.data.Users == nil {
		s.data.Users = map[string]*User{}
	}
	if s.data.Trash == nil {
		s.data.Trash = map[string]*TrashEntry{}
	}
	if s.data.Uploads == nil {
		s.data.Uploads = map[string]*UploadSession{}
	}
	// 兼容旧数据：把普通账号上残留的管理员专属权限位（回收站/日志/用户管理）清掉，
	// 否则 Validate 会拒绝加载，用户升级后直接起不来。
	changed := false
	for _, u := range s.data.Users {
		if u == nil || u.IsAdmin {
			continue
		}
		for p := range u.Permissions {
			if !ValidUserPermission(p) {
				delete(u.Permissions, p)
				changed = true
			}
		}
	}
	if changed {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// saveLocked 原子落盘。调用方必须持有写锁。
func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // rename 成功后这里是空操作
	}()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Save 显式落盘（一般情况下不需要，各写方法已自带）。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// ---------------------------------------------------------------- 用户

// Users 返回全部用户的拷贝，按用户名排序。
func (s *Store) Users() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.data.Users))
	for _, u := range s.data.Users {
		out = append(out, u.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// User 返回单个用户的拷贝。
func (s *Store) User(name string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.data.Users[strings.ToLower(name)]
	if !ok {
		return nil, false
	}
	return u.Clone(), true
}

// UserCount 返回用户总数。
func (s *Store) UserCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.Users)
}

// AdminCount 返回启用状态的管理员数量。
func (s *Store) AdminCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, u := range s.data.Users {
		if u.IsAdmin && !u.Disabled {
			n++
		}
	}
	return n
}

// PutUser 新增或覆盖用户。
func (s *Store) PutUser(u *User) error {
	if u == nil || u.Username == "" {
		return errors.New("用户名不能为空")
	}
	if err := u.Validate(); err != nil {
		return err
	}
	key := strings.ToLower(u.Username)
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.data.Users[key]; ok {
		if u.CreatedAt.IsZero() {
			u.CreatedAt = old.CreatedAt
		}
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	u.UpdatedAt = time.Now().UTC()
	s.data.Users[key] = u.Clone()
	return s.saveLocked()
}

// DeleteUser 删除用户（不删除其文件）。
func (s *Store) DeleteUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(name)
	if _, ok := s.data.Users[key]; !ok {
		return ErrNotFound
	}
	delete(s.data.Users, key)
	return s.saveLocked()
}

// ---------------------------------------------------------------- 回收站

// TrashEntries 返回回收站记录拷贝，按删除时间倒序。
func (s *Store) TrashEntries() []*TrashEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*TrashEntry, 0, len(s.data.Trash))
	for _, e := range s.data.Trash {
		c := *e
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out
}

// TrashEntry 按 ID 取记录。
func (s *Store) TrashEntry(id string) (*TrashEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data.Trash[id]
	if !ok {
		return nil, false
	}
	c := *e
	return &c, true
}

// PutTrashEntry 新增/覆盖回收站记录。
func (s *Store) PutTrashEntry(e *TrashEntry) error {
	if e == nil || e.ID == "" {
		return errors.New("回收站记录 ID 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *e
	s.data.Trash[e.ID] = &c
	return s.saveLocked()
}

// DeleteTrashEntry 删除回收站记录。
func (s *Store) DeleteTrashEntry(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Trash[id]; !ok {
		return ErrNotFound
	}
	delete(s.data.Trash, id)
	return s.saveLocked()
}

// TrashUsedByRoot 汇总某个根目录的回收站占用。
func (s *Store) TrashUsedByRoot(root string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, e := range s.data.Trash {
		if e.Root == root {
			total += e.Size
		}
	}
	return total
}

// ---------------------------------------------------------------- 上传会话

// UploadSessions 返回上传会话拷贝。
func (s *Store) UploadSessions() []*UploadSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*UploadSession, 0, len(s.data.Uploads))
	for _, u := range s.data.Uploads {
		out = append(out, u.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// UploadSession 按 ID 取会话。
func (s *Store) UploadSession(id string) (*UploadSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.data.Uploads[id]
	if !ok {
		return nil, false
	}
	return u.Clone(), true
}

// FindResumable 查找同一用户、同一目标、同名的未过期会话。
func (s *Store) FindResumable(username, dir, name string, size int64) (*UploadSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, u := range s.data.Uploads {
		if u.Username == username && u.Dir == dir && u.Name == name && u.Size == size && now.Before(u.ExpiresAt) {
			return u.Clone(), true
		}
	}
	return nil, false
}

// PutUploadSession 新增/覆盖上传会话。
func (s *Store) PutUploadSession(u *UploadSession) error {
	if u == nil || u.ID == "" {
		return errors.New("上传会话 ID 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := u.Clone()
	sort.Ints(c.Received)
	s.data.Uploads[u.ID] = c
	return s.saveLocked()
}

// MarkChunkReceived 幂等地把分片下标标记为已接收。
func (s *Store) MarkChunkReceived(id string, index int) (*UploadSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.data.Uploads[id]
	if !ok {
		return nil, ErrNotFound
	}
	if index < 0 || index >= u.ChunkCount {
		return nil, fmt.Errorf("分片下标 %d 超出范围 0..%d", index, u.ChunkCount-1)
	}
	for _, i := range u.Received {
		if i == index {
			return u.Clone(), nil
		}
	}
	u.Received = append(u.Received, index)
	sort.Ints(u.Received)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return u.Clone(), nil
}

// DeleteUploadSession 删除上传会话记录。
func (s *Store) DeleteUploadSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Uploads[id]; !ok {
		return ErrNotFound
	}
	delete(s.data.Uploads, id)
	return s.saveLocked()
}
