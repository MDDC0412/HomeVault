package core

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"homevault/internal/audit"
	"homevault/internal/state"
)

func shaOf(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// uploadsBytes 统计某个用户待提交的分片数据占用的磁盘字节数。
func (f *fix) uploadsBytes(id string) int64 {
	f.t.Helper()
	var total int64
	_ = filepath.Walk(filepath.Join(f.cfg.UploadPath(), id), func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

func TestChunkedUploadHappyPath(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "big.bin", 300_000, 100_000)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	if sess.ChunkCount != 3 || sess.ChunkSize != 100_000 || sess.Size != 300_000 {
		t.Fatalf("会话参数不正确：%+v", sess)
	}
	if sess.ExpiresAt.Before(time.Now()) {
		t.Fatalf("会话过期时间应在未来：%v", sess.ExpiresAt)
	}
	// 乱序 + 重复写入
	parts := [][]byte{bytes.Repeat([]byte("A"), 100_000), bytes.Repeat([]byte("B"), 100_000), bytes.Repeat([]byte("C"), 100_000)}
	// 同一分片写两次必须幂等
	got, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(parts[0]), 100_000, shaOf(parts[0]))
	if err != nil {
		t.Fatalf("WriteChunk(0): %v", err)
	}
	if len(got.Received) != 1 {
		t.Fatalf("首次写入后应记录 1 个分片：%v", got.Received)
	}
	got, err = f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(parts[0]), 100_000, shaOf(parts[0]))
	if err != nil {
		t.Fatalf("重复写入同一分片应成功：%v", err)
	}
	if len(got.Received) != 1 {
		t.Fatalf("重复写入同一分片不应重复计数：%v", got.Received)
	}
	// 其余分片乱序写入
	for _, idx := range []int{2, 1} {
		if _, err := f.svc.WriteChunk(f.alice, sess.ID, idx, bytes.NewReader(parts[idx]), int64(len(parts[idx])), shaOf(parts[idx])); err != nil {
			t.Fatalf("WriteChunk(%d): %v", idx, err)
		}
	}
	// 已接收的分片再写一次仍然幂等
	if got, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(parts[0]), 100_000, shaOf(parts[0])); err != nil {
		t.Fatal(err)
	} else if len(got.Received) != 3 {
		t.Fatalf("全部分片接收后应记录 3 个：%v", got.Received)
	}
	_, rel, size, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if rel != "big.bin" || size != 300_000 {
		t.Fatalf("完成结果不正确：%q %d", rel, size)
	}
	abs, err := f.svc.Resolve(f.alice, "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 300_000 || !bytes.Equal(raw[:100_000], parts[0]) ||
		!bytes.Equal(raw[100_000:200_000], parts[1]) || !bytes.Equal(raw[200_000:], parts[2]) {
		t.Fatal("合并后的内容或顺序不正确")
	}
	if _, ok := f.st.UploadSession(sess.ID); ok {
		t.Fatal("完成后会话应被删除")
	}
	if _, err := os.Stat(filepath.Join(f.cfg.UploadPath(), sess.ID)); !os.IsNotExist(err) {
		t.Fatalf("完成后分片目录应被清理：%v", err)
	}
	if used, _, err := f.svc.Usage(f.alice); err != nil || used != 300_000 {
		t.Fatalf("完成后用量应刷新为 300000，实际 %d err=%v", used, err)
	}
	// 会话已不存在
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(nil), 0, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("会话不存在应返回 ErrNotFound，实际 %v", err)
	}
}

func TestZeroByteUpload(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "empty.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ChunkCount != 1 {
		t.Fatalf("0 字节文件应只有 1 个分片，实际 %d", sess.ChunkCount)
	}
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(nil), 0, shaOf(nil)); err != nil {
		t.Fatalf("写入 0 字节分片：%v", err)
	}
	if _, _, size, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); err != nil || size != 0 {
		t.Fatalf("0 字节上传应成功：%d %v", size, err)
	}
	abs, _ := f.svc.Resolve(f.alice, "empty.txt")
	if fi, err := os.Stat(abs); err != nil || fi.Size() != 0 {
		t.Fatalf("应生成 0 字节文件：%v", err)
	}
}

func TestWriteChunkValidation(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "x.bin", 300_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("A"), 100_000)
	// 长度不符（声明）
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), 99_999, ""); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("声明长度不符应报 ErrBadRequest，实际 %v", err)
	}
	// 实际字节数比声明少
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data[:10]), 100_000, ""); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("实际长度不符应报 ErrBadRequest，实际 %v", err)
	}
	// 下标越界（负数 / 超过分片数）
	for _, idx := range []int{-1, 3, 99} {
		if _, err := f.svc.WriteChunk(f.alice, sess.ID, idx, bytes.NewReader(data), 100_000, ""); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("下标 %d 应报 ErrBadRequest，实际 %v", idx, err)
		}
	}
	// 最后一个分片长度必须精确
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 2, bytes.NewReader(data), 100_000, ""); err != nil {
		t.Fatalf("末片等长时应成功：%v", err)
	}
	// SHA 校验
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), 100_000, strings.Repeat("0", 64)); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("SHA 不符应报 ErrBadRequest，实际 %v", err)
	}
	// 失败不应留下分片
	if got := f.uploadsBytes(sess.ID); got != 100_000 {
		t.Fatalf("失败的分片不应落盘，当前占用 %d", got)
	}
	// 正确的 SHA（大小写不敏感）
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), 100_000, strings.ToUpper(shaOf(data))); err != nil {
		t.Fatalf("大写 SHA 应被接受：%v", err)
	}
}

func TestCompleteUploadValidation(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "y.bin", 200_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("A"), 100_000)
	// 缺分片
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); !errors.Is(err, ErrUploadState) {
		t.Fatalf("缺分片应报 ErrUploadState，实际 %v", err)
	}
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), 100_000, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); !errors.Is(err, ErrUploadState) {
		t.Fatalf("仍缺分片应报 ErrUploadState，实际 %v", err)
	}
	// 手动补上第二片，但把分片文件改短 → 合并大小不符
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 1, bytes.NewReader(data), 100_000, ""); err != nil {
		t.Fatal(err)
	}
	chunk1 := filepath.Join(f.cfg.UploadPath(), sess.ID, "chunk-1")
	if err := os.Truncate(chunk1, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); !errors.Is(err, ErrUploadState) {
		t.Fatalf("合并大小不符应报 ErrUploadState，实际 %v", err)
	}
	// 分片文件被删除
	if err := os.Remove(chunk1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); !errors.Is(err, ErrUploadState) {
		t.Fatalf("分片缺失应报 ErrUploadState，实际 %v", err)
	}
	// 目标目录消失
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 1, bytes.NewReader(data), 100_000, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.cfg.FilesPath(), "alice")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("目标目录不存在应报 ErrNotFound，实际 %v", err)
	}
	// 不能完成别人的会话
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.bob, sess.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非本人不能完成会话，实际 %v", err)
	}
	// 管理员"不带目标用户"时没有自己的存储空间，不能碰别人的会话（D6 修复）。
	// HTTP 层用 ?user=alice 时传给 core 的 u 就是 alice，因此代操作链路仍然可用。
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.admin, sess.ID); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("管理员以自己的身份完成会话应因缺少存储空间被拒，实际 %v", err)
	}
	if _, err := f.svc.UploadSession(f.alice, sess.ID); err != nil {
		t.Fatalf("代操作目标用户（u=alice）应能取到会话：%v", err)
	}
}

// 提交阶段的配额校验不能把本次会话的待提交分片算两遍（回归用例 #N1）。
func TestCompleteUploadWithinQuotaSucceeds(t *testing.T) {
	regression(t, "#N1",
		"CompleteUpload（core/service.go:816）调用 CheckQuota(u, sess.Size)，而 usage() 已把 "+
			"PendingUploadBytes（service.go:163,168-182）计入 used —— 同一个会话的待提交分片被算了两遍。"+
			"后果：只要单文件 size > quota/2，分片写入时通过（WriteChunk 的 CheckQuota 只算一次），"+
			"提交时却因为 used 里已经含有这批分片而 507，文件永远无法完成。"+
			"建议：提交时改为 CheckQuota(u, sess.Size-该会话已占用字节)，或让 usage 支持排除当前会话")
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 1000
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "half.bin", 600, 0)
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	data := bytes.Repeat([]byte("h"), 600)
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), 600, shaOf(data)); err != nil {
		t.Fatalf("写入分片：%v", err)
	}
	if _, _, size, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); err != nil {
		t.Fatalf("600 字节文件远在 1000 字节配额之内，必须能完成，实际 %v", err)
	} else if size != 600 {
		t.Fatalf("完成大小应为 600，实际 %d", size)
	}
}

func TestInitUploadReuseAndLimits(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	a, err := f.svc.InitUpload(f.act(), f.alice, "", "same.bin", 200_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.InitUpload(f.act(), f.alice, "", "same.bin", 200_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("同名同大小应复用会话：%s vs %s", a.ID, b.ID)
	}
	if c, err := f.svc.InitUpload(f.act(), f.alice, "", "same.bin", 200_001, 0); err != nil || c.ID == a.ID {
		t.Fatalf("大小不同不应复用：%v %v", c, err)
	}
	if d, err := f.svc.InitUpload(f.act(), f.alice, "", "other.bin", 200_000, 0); err != nil || d.ID == a.ID {
		t.Fatalf("名字不同不应复用：%v %v", d, err)
	}
	// 过期的会话不会被复用
	expired := &state.UploadSession{ID: "u_expired", Username: "alice", Dir: "", Name: "old.bin",
		Size: 200_000, ChunkSize: 100_000, ChunkCount: 2, CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-time.Hour)}
	if err := f.st.PutUploadSession(expired); err != nil {
		t.Fatal(err)
	}
	e, err := f.svc.InitUpload(f.act(), f.alice, "", "old.bin", 200_000, 0)
	if err != nil || e.ID == "u_expired" {
		t.Fatalf("过期会话不应被复用：%v %v", e, err)
	}

	// 参数校验
	if _, err := f.svc.InitUpload(f.act(), f.alice, "", "neg.bin", -1, 0); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("负 size 应报 ErrBadRequest，实际 %v", err)
	}
	if _, err := f.svc.InitUpload(f.act(), f.alice, "", "bad/name.bin", 10, 0); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("非法文件名应报 ErrBadRequest，实际 %v", err)
	}
	if _, err := f.svc.InitUpload(f.act(), f.alice, "nodir", "z.bin", 10, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("目标目录不存在应报 ErrNotFound，实际 %v", err)
	}
	if _, err := f.svc.InitUpload(f.act(), f.alice, "../bob", "z.bin", 10, 0); err != nil {
		// 越界的 dir 会被夹回自己的根，同样必须落在 alice 下
		abs, _ := f.svc.Resolve(f.alice, "z.bin")
		// 同上：TempDir 的 8.3 短名要先规范化再比对前缀
		if !strings.HasPrefix(evalPath(abs), evalPath(filepath.Join(f.cfg.FilesPath(), "alice"))) {
			t.Fatalf("上传目标越界：%s", abs)
		}
	}

	// 单文件上限
	st := state.DefaultSettings()
	st.ChunkSize = state.SizeValue(64 << 10)
	st.MaxUploadSize = state.SizeValue(100_000)
	f.putSettings(st)
	if _, err := f.svc.InitUpload(f.act(), f.alice, "", "toolarge.bin", 100_001, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超过单文件上限应报 ErrTooLarge，实际 %v", err)
	}
	if _, err := f.svc.InitUpload(f.act(), f.alice, "", "ok.bin", 100_000, 0); err != nil {
		t.Fatalf("恰好等于上限应允许：%v", err)
	}

	// 配额
	f.alice.Quota = 1000
	if _, err := f.svc.InitUpload(f.act(), f.alice, "", "quota.bin", 1001, 0); !errors.Is(err, ErrQuota) {
		t.Fatalf("初始化时超配额应报 ErrQuota，实际 %v", err)
	}
}

func TestUploadSessionOwnershipAndExpiry(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "own.bin", 100_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UploadSession(f.bob, sess.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("别人取会话应报 ErrForbidden，实际 %v", err)
	}
	// 管理员"不带目标用户"时没有存储空间，同样不能取别人的会话（D6 修复）
	if _, err := f.svc.UploadSession(f.admin, sess.ID); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("无存储空间的管理员取他人会话应报 ErrNoStorage，实际 %v", err)
	}
	if got, err := f.svc.UploadSession(f.alice, sess.ID); err != nil || got.ID != sess.ID {
		t.Fatalf("本人应可取会话：%v %v", got, err)
	}
	if _, err := f.svc.UploadSession(f.alice, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的会话应报 ErrNotFound，实际 %v", err)
	}
	// 过期后写入 / 完成都应被拒
	sess.ExpiresAt = time.Now().Add(-time.Minute)
	if err := f.st.PutUploadSession(sess); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("A"), 100_000)
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), 100_000, ""); !errors.Is(err, ErrUploadState) {
		t.Fatalf("过期后写入应报 ErrUploadState，实际 %v", err)
	}
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); !errors.Is(err, ErrUploadState) {
		t.Fatalf("过期后完成应报 ErrUploadState，实际 %v", err)
	}
	if err := f.svc.CancelUpload(f.act(), f.alice, sess.ID); err != nil {
		t.Fatalf("取消过期会话应仍然可用：%v", err)
	}
	if _, ok := f.st.UploadSession(sess.ID); ok {
		t.Fatal("取消后会话应被删除")
	}
	// 别人的会话不能取消
	s2, err := f.svc.InitUpload(f.act(), f.alice, "", "own2.bin", 100_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CancelUpload(f.act(), f.bob, s2.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("取消别人的会话应报 ErrForbidden，实际 %v", err)
	}
}

func TestCancelUploadCleansChunks(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "cancel.bin", 200_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(bytes.Repeat([]byte("A"), 100_000)), 100_000, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CancelUpload(f.act(), f.alice, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.UploadPath(), sess.ID)); !os.IsNotExist(err) {
		t.Fatalf("取消后分片目录应被清理：%v", err)
	}
	if _, ok := f.st.UploadSession(sess.ID); ok {
		t.Fatal("取消后会话记录应被删除")
	}
}

func TestCompleteUploadRechecksQuota(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 1000
	sess, err := f.svc.InitUpload(f.act(), f.alice, "", "q.bin", 1000, 1000)
	if err != nil {
		t.Fatalf("初始化时未超配额，应成功：%v", err)
	}
	data := bytes.Repeat([]byte("A"), 1000)
	if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), 1000, ""); err != nil {
		t.Fatal(err)
	}
	// 期间磁盘被填满
	f.write(f.alice, "fill.bin", strings.Repeat("z", 1000))
	f.svc.InvalidateUsage(f.alice.Root)
	if _, _, _, err := f.svc.CompleteUpload(f.act(), f.alice, sess.ID); !errors.Is(err, ErrQuota) {
		t.Fatalf("完成时超配额应报 ErrQuota，实际 %v", err)
	}
	// 失败后会话应保留，便于用户清理后重试
	if _, ok := f.st.UploadSession(sess.ID); !ok {
		t.Fatal("配额失败后会话不应被删除")
	}
	if f.uploadsBytes(sess.ID) != 1000 {
		t.Fatal("配额失败后分片应保留")
	}
	if _, err := os.Stat(filepath.Join(f.cfg.FilesPath(), "alice", "q.bin")); !os.IsNotExist(err) {
		t.Fatalf("配额失败后不应落盘：%v", err)
	}
}

// 待提交的分片数据必须计入配额（D4 修复）：否则同一用户可以并发开启任意多个会话，
// 把"已声明但未提交"的数据堆满磁盘，直到完成时才校验。
func TestPendingUploadsAreCountedAgainstQuota(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 1000
	s1, err := f.svc.InitUpload(f.act(), f.alice, "", "p1.bin", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("A"), 1000)
	if _, err := f.svc.WriteChunk(f.alice, s1.ID, 0, bytes.NewReader(data), 1000, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.uploadsBytes(s1.ID); got != 1000 {
		t.Fatalf("前提：已堆下 1000 字节等待提交，实际 %d", got)
	}
	// 配额是 1000，磁盘上已有 1000 字节待提交数据 → 再申请 1000 字节必须被拒
	if err := f.svc.CheckQuota(f.alice, 1000); !errors.Is(err, ErrQuota) {
		t.Fatalf("存在 1000 字节待提交数据时应已用满配额，实际 %v", err)
	}
	if _, err := f.svc.InitUpload(f.act(), f.alice, "", "p2.bin", 1000, 1000); !errors.Is(err, ErrQuota) {
		t.Fatalf("第二个会话应被配额拒绝，实际 %v", err)
	}
}

func TestCleanupUploadSessionsAndOrphans(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0
	live, err := f.svc.InitUpload(f.act(), f.alice, "", "live.bin", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	dead := &state.UploadSession{ID: "u_dead", Username: "alice", Name: "dead.bin", Size: 10,
		ChunkSize: 10, ChunkCount: 1, CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}
	if err := f.st.PutUploadSession(dead); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.cfg.UploadPath(), "u_dead"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.cfg.UploadPath(), "u_dead", "chunk-0"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	// 孤儿目录（磁盘上有、state 里没有）
	orphan := filepath.Join(f.cfg.UploadPath(), "u_orphan")
	if err := os.MkdirAll(orphan, 0o750); err != nil {
		t.Fatal(err)
	}
	n, err := f.svc.CleanupUploadSessions()
	if err != nil || n != 1 {
		t.Fatalf("应清理 1 个过期会话，实际 %d err=%v", n, err)
	}
	if _, ok := f.st.UploadSession("u_dead"); ok {
		t.Fatal("过期会话记录应被删除")
	}
	if _, ok := f.st.UploadSession(live.ID); !ok {
		t.Fatal("未过期会话不应被清理")
	}
	if err := f.svc.CleanupExpiredUploadsDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("孤儿目录应被清理：%v", err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.UploadPath(), live.ID)); err != nil {
		t.Fatalf("有效会话目录不应被清理：%v", err)
	}
}

func TestConcurrentUploadsKeepUsageConsistent(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.alice.Quota = 0 // 不限配额，专注校验并发一致性
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("c%d.bin", i)
			data := bytes.Repeat([]byte{byte('a' + i)}, 1000)
			sess, err := f.svc.InitUpload(Actor{Username: "alice"}, f.alice, "", name, int64(len(data)), int64(len(data)))
			if err != nil {
				errs[i] = err
				return
			}
			if _, err := f.svc.WriteChunk(f.alice, sess.ID, 0, bytes.NewReader(data), int64(len(data)), shaOf(data)); err != nil {
				errs[i] = err
				return
			}
			if _, _, _, err := f.svc.CompleteUpload(Actor{Username: "alice"}, f.alice, sess.ID); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发上传 %d 失败：%v", i, err)
		}
	}
	f.svc.InvalidateUsage(f.alice.Root)
	used, _, err := f.svc.Usage(f.alice)
	if err != nil {
		t.Fatal(err)
	}
	if used != workers*1000 {
		t.Fatalf("并发上传后用量应为 %d，实际 %d", workers*1000, used)
	}
	// 审计应恰好记录 workers 条上传
	res, err := f.logs.Query(audit.Query{Action: audit.ActionUpload, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != workers {
		t.Fatalf("应记录 %d 条上传审计，实际 %d", workers, res.Total)
	}
}
