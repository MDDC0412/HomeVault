package state_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homevault/internal/state"
)

// regression：这些用例原本用于复现已知缺陷；缺陷修复后改为默认运行的回归用例，id/desc 只用于回溯。
func regression(t *testing.T, id, desc string) {
	t.Helper()
	// 该缺陷已修复，用例默认运行；id/desc 仅用于回溯历史。
	t.Logf("回归用例 %s：%s", id, desc)
}

func newStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("state.Open 失败：%v", err)
	}
	return st
}

func user(name, root string) *state.User {
	return &state.User{Username: name, Root: root, PasswordHash: "pbkdf2-sha256$1$AA$BB", Permissions: map[string]bool{}}
}

func TestValidateUsername(t *testing.T) {
	ok := []string{"abc", "ABC", "a_b", "a-b", "_ab", "-ab", "a1234567890123456789012345678901"} // 32 位
	for _, n := range ok {
		if err := state.ValidateUsername(n); err != nil {
			t.Fatalf("用户名 %q 应合法：%v", n, err)
		}
	}
	bad := []string{"", "a", "ab", "中文名", "a b", "a.b", "a@b", "a/b", "a\\b", "a\x00b",
		"a12345678901234567890123456789012"} // 33 位
	for _, n := range bad {
		if err := state.ValidateUsername(n); err == nil {
			t.Fatalf("用户名 %q 应被拒绝", n)
		}
	}
}

func TestUserValidateRules(t *testing.T) {
	// 管理员必须没有存储空间
	rootAdmin := user("admin", "")
	rootAdmin.IsAdmin = true
	if err := rootAdmin.Validate(); err != nil {
		t.Fatalf("管理员 root 为空应通过：%v", err)
	}
	a := user("admin", "x")
	a.IsAdmin = true
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "root 必须为空") {
		t.Fatalf("管理员带 root 必须报错，实际 %v", err)
	}
	a = user("admin", "   ")
	a.IsAdmin = true
	if err := a.Validate(); err != nil {
		t.Fatalf("只有空白的 root 视为空：%v", err)
	}
	// 普通用户必须有存储空间
	if err := user("alice", "alice").Validate(); err != nil {
		t.Fatalf("普通用户带 root 应通过：%v", err)
	}
	if err := user("alice", "").Validate(); err == nil {
		t.Fatal("普通用户 root 为空必须报错")
	}
	if err := user("alice", "  ").Validate(); err == nil {
		t.Fatal("普通用户 root 只有空白必须报错")
	}
	// 负配额 / 负限速
	u := user("alice", "alice")
	u.Quota = -1
	if err := u.Validate(); err == nil {
		t.Fatal("负配额必须报错")
	}
	u = user("alice", "alice")
	u.RateLimitMbps = -1
	if err := u.Validate(); err == nil {
		t.Fatal("负限速必须报错")
	}
	// 用户名非法
	bad := user("a", "a")
	if err := bad.Validate(); err == nil {
		t.Fatal("非法用户名必须报错")
	}
}

func TestUserCanAndPermListAndClone(t *testing.T) {
	var nilUser *state.User
	if nilUser.Can(state.PermRead) {
		t.Fatal("nil 用户不能拥有权限")
	}
	u := user("alice", "alice")
	u.Permissions = map[string]bool{state.PermRead: true}
	if !u.Can(state.PermRead) || u.Can(state.PermDelete) {
		t.Fatal("普通用户权限判断错误")
	}
	u.Disabled = true
	if u.Can(state.PermRead) {
		t.Fatal("被禁用用户不能拥有任何权限")
	}
	u.Disabled = false
	admin := user("admin", "")
	admin.IsAdmin = true
	for _, p := range state.AllPermissions {
		if !admin.Can(p) {
			t.Fatalf("管理员应隐含拥有 %s", p)
		}
	}
	if got := admin.PermList(); len(got) != len(state.AllPermissions) {
		t.Fatalf("管理员权限列表应有 %d 项，实际 %d", len(state.AllPermissions), len(got))
	}
	got := u.PermList()
	if len(got) != 1 || got[0] != state.PermRead {
		t.Fatalf("权限列表应为 [read]，实际 %v", got)
	}
	if !state.ValidPermission(state.PermRead) || state.ValidPermission("bogus") {
		t.Fatal("ValidPermission 判断错误")
	}
	// Clone 必须深拷贝 map
	c := u.Clone()
	c.Permissions[state.PermDelete] = true
	c.Username = "other"
	if u.Permissions[state.PermDelete] || u.Username != "alice" {
		t.Fatal("Clone 不是深拷贝")
	}
}

func TestStoreUserCRUDAndPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(user("Alice", "alice")); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(user("bob", "bob")); err != nil {
		t.Fatal(err)
	}
	// 大小写不敏感查找 + 返回拷贝
	got, ok := st.User("ALICE")
	if !ok || got.Username != "Alice" {
		t.Fatalf("用户名查找应不区分大小写：%v %v", got, ok)
	}
	got.Root = "hacked"
	if again, _ := st.User("alice"); again.Root != "alice" {
		t.Fatal("User() 必须返回拷贝")
	}
	// 非法用户不得落盘
	if err := st.PutUser(user("bad name", "x")); err == nil {
		t.Fatal("非法用户名应被 PutUser 拒绝")
	}
	if st.UserCount() != 2 {
		t.Fatalf("用户数应为 2，实际 %d", st.UserCount())
	}
	// 排序
	us := st.Users()
	if len(us) != 2 || us[0].Username != "Alice" || us[1].Username != "bob" {
		t.Fatalf("Users() 应按用户名排序：%+v", us)
	}
	// CreatedAt 保留、UpdatedAt 刷新
	orig := us[0].CreatedAt
	time.Sleep(2 * time.Millisecond)
	upd := user("Alice", "alice")
	upd.CreatedAt = time.Time{}
	if err := st.PutUser(upd); err != nil {
		t.Fatal(err)
	}
	after, _ := st.User("alice")
	if !after.CreatedAt.Equal(orig) {
		t.Fatalf("覆盖时必须保留 CreatedAt：%v != %v", after.CreatedAt, orig)
	}
	if !after.UpdatedAt.After(orig) && !after.UpdatedAt.Equal(orig) {
		t.Fatalf("UpdatedAt 应被刷新：%v", after.UpdatedAt)
	}
	// 管理员计数
	adm := user("admin", "")
	adm.IsAdmin = true
	if err := st.PutUser(adm); err != nil {
		t.Fatal(err)
	}
	adm2 := user("admin2", "")
	adm2.IsAdmin = true
	adm2.Disabled = true
	if err := st.PutUser(adm2); err != nil {
		t.Fatal(err)
	}
	if n := st.AdminCount(); n != 1 {
		t.Fatalf("AdminCount 应只数未禁用的管理员，实际 %d", n)
	}
	// 删除
	if err := st.DeleteUser("BOB"); err != nil {
		t.Fatalf("删除应不区分大小写：%v", err)
	}
	if _, ok := st.User("bob"); ok {
		t.Fatal("bob 应已被删除")
	}
	if err := st.DeleteUser("ghost"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("删除不存在的用户应返回 ErrNotFound，实际 %v", err)
	}

	// 重新 Open 后数据仍在
	st2, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "admin", "admin2"} {
		if _, ok := st2.User(name); !ok {
			t.Fatalf("重新打开后用户 %s 丢失", name)
		}
	}
	if _, ok := st2.User("bob"); ok {
		t.Fatal("重新打开后被删除的 bob 又出现了")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".state-") {
			t.Fatalf("落盘后不应残留临时文件：%s", e.Name())
		}
	}
}

func TestStoreTrash(t *testing.T) {
	st := newStore(t)
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	if err := st.PutTrashEntry(&state.TrashEntry{ID: "t1", Name: "a", Root: "alice", Size: 100, DeletedAt: now.Add(-time.Hour), ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTrashEntry(&state.TrashEntry{ID: "t2", Name: "b", Root: "alice", Size: 200, DeletedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTrashEntry(&state.TrashEntry{ID: "t3", Name: "c", Root: "bob", Size: 1000, DeletedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTrashEntry(&state.TrashEntry{ID: ""}); err == nil {
		t.Fatal("空 ID 必须报错")
	}
	if got := st.TrashUsedByRoot("alice"); got != 300 {
		t.Fatalf("alice 的回收站占用应为 300，实际 %d", got)
	}
	// 只统计完全相等的 root（大小写敏感、不做前缀匹配）
	if got := st.TrashUsedByRoot("ALICE"); got != 0 {
		t.Fatalf("root 比较是精确字符串比较，实际 %d", got)
	}
	if got := st.TrashUsedByRoot("al"); got != 0 {
		t.Fatalf("root 不做前缀匹配，实际 %d", got)
	}
	entries := st.TrashEntries()
	if len(entries) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(entries))
	}
	if entries[0].ID == "t1" {
		t.Fatalf("应按 DeletedAt 倒序，实际 %+v", entries)
	}
	// 返回拷贝
	got, ok := st.TrashEntry("t1")
	if !ok {
		t.Fatal("t1 应存在")
	}
	got.Size = 999
	if again, _ := st.TrashEntry("t1"); again.Size != 100 {
		t.Fatal("TrashEntry 必须返回拷贝")
	}
	if err := st.DeleteTrashEntry("t1"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTrashEntry("t1"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound，实际 %v", err)
	}
}

func TestUploadSessionHelpers(t *testing.T) {
	s := &state.UploadSession{Size: 200000, ChunkSize: 65536, ChunkCount: 4}
	if n, err := s.ChunkLength(0); err != nil || n != 65536 {
		t.Fatalf("第 0 片应为 65536：%d %v", n, err)
	}
	if n, _ := s.ChunkLength(3); n != 200000-3*65536 {
		t.Fatalf("最后一片应为余数，实际 %d", n)
	}
	if _, err := s.ChunkLength(4); err == nil {
		t.Fatal("越界分片下标必须报错")
	}
	if _, err := s.ChunkLength(-1); err == nil {
		t.Fatal("负数分片下标必须报错")
	}
	// 整除、单分片、size=0
	s2 := &state.UploadSession{Size: 100, ChunkSize: 100, ChunkCount: 1}
	if n, _ := s2.ChunkLength(0); n != 100 {
		t.Fatalf("单分片应等于 size，实际 %d", n)
	}
	s3 := &state.UploadSession{Size: 0, ChunkSize: 65536, ChunkCount: 1}
	if n, _ := s3.ChunkLength(0); n != 0 {
		t.Fatalf("size=0 时唯一分片长度为 0，实际 %d", n)
	}
	s.Received = []int{0, 3}
	if got := s.BytesReceived(); got != 65536+3392 {
		t.Fatalf("BytesReceived 计算错误：%d", got)
	}
	if s.Complete() {
		t.Fatal("只收到 2/4 片不应 Complete")
	}
	s.Received = []int{0, 1, 2, 3}
	if !s.Complete() {
		t.Fatal("4/4 片应 Complete")
	}
	// Clone 深拷贝 Received
	c := s.Clone()
	c.Received[0] = 99
	if s.Received[0] != 0 {
		t.Fatal("Clone 必须深拷贝 Received")
	}
}

func TestStoreUploadSessions(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	sess := &state.UploadSession{
		ID: "u1", Username: "alice", Dir: "docs", Name: "big.bin", Size: 1000,
		ChunkSize: 64 << 10, ChunkCount: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := st.PutUploadSession(sess); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUploadSession(&state.UploadSession{ID: ""}); err == nil {
		t.Fatal("空 ID 必须报错")
	}
	// Received 会去重排序后存储
	if _, err := st.MarkChunkReceived("ghost", 0); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("不存在的会话应返回 ErrNotFound，实际 %v", err)
	}
	if s, ok := st.FindResumable("alice", "docs", "big.bin", 1000); !ok || s.ID != "u1" {
		t.Fatalf("应能找到可续传会话：%+v %v", s, ok)
	}
	if _, ok := st.FindResumable("alice", "docs", "big.bin", 999); ok {
		t.Fatal("size 不同不应复用")
	}
	if _, ok := st.FindResumable("bob", "docs", "big.bin", 1000); ok {
		t.Fatal("不同用户不应复用")
	}
	// 过期会话不能被复用
	expired := &state.UploadSession{ID: "u2", Username: "alice", Dir: "docs", Name: "old.bin", Size: 5, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	if err := st.PutUploadSession(expired); err != nil {
		t.Fatal(err)
	}
	if s, ok := st.FindResumable("alice", "docs", "old.bin", 5); ok {
		t.Fatalf("过期会话不应被复用：%+v", s)
	}
	sessions := st.UploadSessions()
	if len(sessions) != 2 {
		t.Fatalf("应有 2 个会话，实际 %d", len(sessions))
	}
	if sessions[0].CreatedAt.Before(sessions[1].CreatedAt) {
		t.Fatal("UploadSessions 应按 CreatedAt 倒序")
	}
	if err := st.DeleteUploadSession("u1"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUploadSession("u1"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound，实际 %v", err)
	}
}

// MarkChunkReceived 必须校验分片下标范围（D16 修复），否则可以为不存在的分片"记账"。
func TestMarkChunkReceivedRejectsOutOfRangeIndex(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	if err := st.PutUploadSession(&state.UploadSession{
		ID: "u1", Username: "alice", Name: "x", Size: 100, ChunkSize: 100, ChunkCount: 1,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkChunkReceived("u1", 3); err == nil {
		t.Fatal("下标越界的分片必须被拒绝")
	}
	if _, err := st.MarkChunkReceived("u1", -1); err == nil {
		t.Fatal("负数下标必须被拒绝")
	}
	if s, _ := st.UploadSession("u1"); s.Complete() {
		t.Fatal("没收到真正的分片 0 就不该 Complete")
	}
	if _, err := st.MarkChunkReceived("u1", 0); err != nil {
		t.Fatalf("合法下标应被接受：%v", err)
	}
	if s, _ := st.UploadSession("u1"); !s.Complete() {
		t.Fatal("收到分片 0 后应 Complete")
	}
}

func TestMarkChunkReceivedIsIdempotent(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	if err := st.PutUploadSession(&state.UploadSession{
		ID: "u1", Username: "alice", Name: "x", Size: 3 * 100, ChunkSize: 100, ChunkCount: 3,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{2, 0, 1, 0, 2} {
		if _, err := st.MarkChunkReceived("u1", idx); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := st.UploadSession("u1")
	if len(s.Received) != 3 {
		t.Fatalf("重复标记必须幂等，实际 %v", s.Received)
	}
	for i, v := range s.Received {
		if i != v {
			t.Fatalf("Received 应升序去重，实际 %v", s.Received)
		}
	}
	if !s.Complete() {
		t.Fatal("三片都到齐应为 Complete")
	}
}

func TestSettingsPersistenceAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 未初始化时返回默认值
	if st.Settings() != state.DefaultSettings() {
		t.Fatalf("未初始化应返回 DefaultSettings，实际 %+v", st.Settings())
	}
	if _, err := st.EnsureSettings(state.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	// 已存在时不覆盖
	custom := state.DefaultSettings()
	custom.DefaultQuota = 123
	if _, err := st.PutSettings(custom); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureSettings(state.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	if got := st.Settings().DefaultQuota; got != 123 {
		t.Fatalf("EnsureSettings 不应覆盖已存在的设置，实际 %d", got)
	}
	// 非法设置不得落盘
	before := st.Settings()
	bad := state.DefaultSettings()
	bad.ChunkSize = 1
	if _, err := st.PutSettings(bad); err == nil {
		t.Fatal("非法设置必须报错")
	}
	if st.Settings() != before {
		t.Fatal("非法设置不应修改内存中的值")
	}
	// 重新打开后设置仍在
	st2, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Settings().DefaultQuota != 123 {
		t.Fatalf("重新打开后设置丢失：%+v", st2.Settings())
	}
	if err := state.DefaultSettings().Validate(); err != nil {
		t.Fatalf("默认设置必须合法：%v", err)
	}
}

func TestSettingsValidate(t *testing.T) {
	mut := func(f func(*state.Settings)) state.Settings {
		s := state.DefaultSettings()
		f(&s)
		return s
	}
	mutOK := func(f func(*state.Settings)) error { return mut(f).Validate() }
	if err := mutOK(func(s *state.Settings) { s.ChunkSize = 65536 }); err != nil {
		t.Fatalf("chunkSize 下限应通过：%v", err)
	}
	if err := mutOK(func(s *state.Settings) { s.ChunkSize = 1 << 30 }); err != nil {
		t.Fatalf("chunkSize 上限应通过：%v", err)
	}
	cases := []struct {
		name string
		s    state.Settings
		want string
	}{
		{"chunkSize 小于 64KiB", mut(func(s *state.Settings) { s.ChunkSize = 65335 }), "chunkSize 至少"},
		{"chunkSize 大于 1GiB", mut(func(s *state.Settings) { s.ChunkSize = 1<<30 + 1 }), "chunkSize 过大"},
		{"maxUploadSize 小于 chunkSize", mut(func(s *state.Settings) { s.MaxUploadSize = 1024 }), "不能小于"},
		{"负默认配额", mut(func(s *state.Settings) { s.DefaultQuota = -1 }), "defaultQuota"},
		{"负默认限速", mut(func(s *state.Settings) { s.DefaultRateLimit = -1 }), "defaultRateLimitMbps"},
		{"负 maxUploadSize", mut(func(s *state.Settings) { s.MaxUploadSize = -1 }), "maxUploadSize"},
		{"相对 spaceDir", mut(func(s *state.Settings) { s.SpaceDir = "rel/dir" }), "绝对路径"},
		{"非法 officePreview", mut(func(s *state.Settings) { s.OfficePreview = "libre" }), "officePreview"},
		{"非法保留期字符串", mut(func(s *state.Settings) { s.TrashRetention = "abc" }), "trashRetention"},
		{"日志保留期非法", mut(func(s *state.Settings) { s.LogRetention = "1x" }), "logRetention"},
		{"会话 TTL 为 0", mut(func(s *state.Settings) { s.UploadSessionTTL = "0" }), "必须大于 0"},
		{"登录会话 TTL 为 0", mut(func(s *state.Settings) { s.SessionTTL = "0" }), "必须大于 0"},
		{"会话 TTL 非法", mut(func(s *state.Settings) { s.SessionTTL = "nope" }), "sessionTTL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.s.Validate()
			if err == nil {
				t.Fatalf("应报错：%+v", c.s)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", c.want, err)
			}
		})
	}
	// 允许为空字符串的 officePreview 与空 spaceDir
	if err := mutOK(func(s *state.Settings) { s.OfficePreview = "" }); err != nil {
		t.Fatalf("officePreview 允许留空：%v", err)
	}
	if err := mutOK(func(s *state.Settings) { s.SpaceDir = "" }); err != nil {
		t.Fatalf("spaceDir 允许留空：%v", err)
	}
	if err := mutOK(func(s *state.Settings) { s.MaxUploadSize = 0 }); err != nil {
		t.Fatalf("maxUploadSize=0 表示不限：%v", err)
	}
	// 保留期为 0（永不清理）合法
	if err := mutOK(func(s *state.Settings) { s.TrashRetention = "0"; s.LogRetention = "0" }); err != nil {
		t.Fatalf("保留期 0 应合法：%v", err)
	}
}

func TestSettingsDurations(t *testing.T) {
	s := state.DefaultSettings()
	if s.TrashDur() != 720*time.Hour {
		t.Fatalf("trashRetention 720h 解析错误：%v", s.TrashDur())
	}
	if s.LogDur() != 2160*time.Hour {
		t.Fatalf("logRetention 2160h 解析错误：%v", s.LogDur())
	}
	if s.UploadSessionDur() != 24*time.Hour {
		t.Fatalf("uploadSessionTTL 解析错误：%v", s.UploadSessionDur())
	}
	if s.SessionDur() != 168*time.Hour {
		t.Fatalf("sessionTTL 解析错误：%v", s.SessionDur())
	}
}

func TestParseSizeString(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"unlimited", 0},
		{"UNLIMITED", 0},
		{"1024", 1024},
		{"512B", 512},
		{"1KiB", 1024},
		{"10GiB", 10 << 30},
		{"500 MB", 500 << 20},
		{"1.5GiB", int64(1.5 * float64(1<<30))},
		{"2TiB", 2 << 40},
		{"1kb", 1024},
		{" 10 GiB ", 10 << 30},
		{"1.5", 1},
	}
	for _, c := range ok {
		got, err := state.ParseSizeString(c.in)
		if err != nil {
			t.Fatalf("ParseSizeString(%q) 意外报错：%v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseSizeString(%q)=%d，期望 %d", c.in, got, c.want)
		}
	}
	bad := []string{"", "  ", "-1", "abc", "10XB", "1e3", "GiB", "10 GiB extra"}
	for _, in := range bad {
		if got, err := state.ParseSizeString(in); err == nil {
			t.Fatalf("ParseSizeString(%q) 应报错，实际 %d", in, got)
		}
	}
	// 非法输入不得退化成 0
	if got, err := state.ParseSizeString("garbage"); err == nil || got != 0 {
		t.Fatalf("非法输入必须报错且不返回 0：%d %v", got, err)
	}
}

func TestSizeValueUnmarshalJSON(t *testing.T) {
	parse := func(s string) (int64, error) {
		var v state.SizeValue
		err := json.Unmarshal([]byte(s), &v)
		return v.Bytes(), err
	}
	if n, err := parse("10737418240"); err != nil || n != 10<<30 {
		t.Fatalf("数字形式解析错误：%d %v", n, err)
	}
	if n, err := parse(`"10GiB"`); err != nil || n != 10<<30 {
		t.Fatalf("字符串形式解析错误：%d %v", n, err)
	}
	if n, err := parse(`"unlimited"`); err != nil || n != 0 {
		t.Fatalf("unlimited 解析错误：%d %v", n, err)
	}
	for _, s := range []string{`null`, `""`, `-1`, `"abc"`, `true`, `1.5`} {
		if n, err := parse(s); err == nil {
			t.Fatalf("JSON %s 应报错，实际 %d", s, n)
		}
	}
}

func TestParseDurationString(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"0", 0},
		{"720h", 720 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"1h30m", 90 * time.Minute},
		{"90m", 90 * time.Minute},
		{"1d12h", 36 * time.Hour},
		{"1w2d", 9 * 24 * time.Hour},
		{"1.5h", 90 * time.Minute},
		{"  24h  ", 24 * time.Hour},
		{"10s", 10 * time.Second},
	}
	for _, c := range ok {
		got, err := state.ParseDurationString(c.in)
		if err != nil {
			t.Fatalf("ParseDurationString(%q) 意外报错：%v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseDurationString(%q)=%v，期望 %v", c.in, got, c.want)
		}
	}
	bad := []string{"", "  ", "-1h", "abc", "1x", "5", "h", "1h30", "1h30x"}
	for _, in := range bad {
		if got, err := state.ParseDurationString(in); err == nil {
			t.Fatalf("ParseDurationString(%q) 应报错，实际 %v", in, got)
		}
	}
}

func TestFormatSizeAndDuration(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1 << 20, "1.00 MiB"},
		{10 << 30, "10.00 GiB"},
		{1 << 30, "1.00 GiB"},
		{1 << 40, "1.00 TiB"},
	}
	for _, c := range cases {
		if got := state.FormatSize(c.in); got != c.want {
			t.Fatalf("FormatSize(%d)=%q，期望 %q", c.in, got, c.want)
		}
	}
	// 往返：FormatSize → ParseSizeString 不应产生比原值更大的数
	for _, n := range []int64{1024, 1536, 10 << 30} {
		if back, err := state.ParseSizeString(state.FormatSize(n)); err != nil || back > n {
			t.Fatalf("FormatSize(%d)=%q 再解析得到 %d err=%v", n, state.FormatSize(n), back, err)
		}
	}
	if got := state.FormatDurationString(90 * time.Minute); got != "1h30m0s" {
		t.Fatalf("FormatDurationString 错误：%q", got)
	}
	if got := state.FormatDurationString(0); got != "0s" {
		t.Fatalf("0 时长格式化错误：%q", got)
	}
}
