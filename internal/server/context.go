package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"homevault/internal/state"
)

// ---------------------------------------------------------------------------
// 请求上下文
// ---------------------------------------------------------------------------

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxTarget
	ctxActingAs
)

func withUser(r *http.Request, u *state.User) context.Context {
	return context.WithValue(r.Context(), ctxUser, u)
}

// withTarget 记录本次请求实际操作的账号（管理员代办时与登录账号不同）。
func withTarget(ctx context.Context, u *state.User, actingAs string) context.Context {
	ctx = context.WithValue(ctx, ctxTarget, u)
	if actingAs != "" {
		ctx = context.WithValue(ctx, ctxActingAs, actingAs)
	}
	return ctx
}

func userFrom(r *http.Request) *state.User {
	u, _ := r.Context().Value(ctxUser).(*state.User)
	return u
}

func targetFrom(r *http.Request) *state.User {
	if u, ok := r.Context().Value(ctxTarget).(*state.User); ok && u != nil {
		return u
	}
	return userFrom(r)
}

func actingAs(r *http.Request) string {
	s, _ := r.Context().Value(ctxActingAs).(string)
	return s
}

// ---------------------------------------------------------------------------
// 速率限制（每用户双向令牌桶）
// ---------------------------------------------------------------------------

// RateLimiter 按 key（用户 + 方向）限制吞吐，单位 Mbps；0 表示不限。
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter 构造限速器。
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{buckets: map[string]*bucket{}}
}

// Wait 在需要时阻塞，直到允许传输 n 字节。
func (l *RateLimiter) Wait(ctx context.Context, key string, mbps int, n int) error {
	if mbps <= 0 || n <= 0 {
		return nil
	}
	rate := float64(mbps) * 125000 // Mbps → 字节/秒
	capacity := rate               // 1 秒的突发额度
	if capacity < 64<<10 {
		capacity = 64 << 10
	}
	for {
		l.mu.Lock()
		b, ok := l.buckets[key]
		now := time.Now()
		if !ok {
			b = &bucket{tokens: capacity, last: now}
			l.buckets[key] = b
		}
		elapsed := now.Sub(b.last).Seconds()
		b.last = now
		b.tokens += elapsed * rate
		if b.tokens > capacity {
			b.tokens = capacity
		}
		if b.tokens >= float64(n) {
			b.tokens -= float64(n)
			l.mu.Unlock()
			return nil
		}
		missing := float64(n) - b.tokens
		wait := time.Duration(missing / rate * float64(time.Second))
		l.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Forget 在用户被删除或限速改变时清理桶。
func (l *RateLimiter) Forget(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

// Reset 清空全部状态（限速设置变更时调用）。
func (l *RateLimiter) Reset() {
	l.mu.Lock()
	l.buckets = map[string]*bucket{}
	l.mu.Unlock()
}

// throttledReadCloser 让请求体按限速阻塞式读取（上传方向）。
type throttledReadCloser struct {
	rc   io.ReadCloser
	rl   *RateLimiter
	ctx  context.Context
	key  string
	mbps int
}

func (t *throttledReadCloser) Read(p []byte) (int, error) {
	if len(p) > 0 {
		if err := t.rl.Wait(t.ctx, t.key, t.mbps, len(p)); err != nil {
			return 0, err
		}
	}
	return t.rc.Read(p)
}

func (t *throttledReadCloser) Close() error { return t.rc.Close() }
