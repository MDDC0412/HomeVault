// Package auth 提供密码哈希、会话令牌与登录限流。
//
// 全部基于标准库实现（crypto/pbkdf2、crypto/hmac、crypto/subtle），不引入第三方依赖。
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PBKDF2 参数。迭代次数偏保守地取 210k，兼顾安全与单核启动/登录延迟。
const (
	pbkdf2Iterations = 210_000
	pbkdf2KeyLen     = 32
	saltLen          = 16
)

// HashPassword 生成 `pbkdf2-sha256$<iters>$<salt-b64>$<hash-b64>` 形式的密码哈希。
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("密码不能为空")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword 校验密码；使用常量时间比较。
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters <= 0 || iters > 5_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iters, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// RandomPassword 生成人类可读的随机密码（用于首次启动时初始化管理员）。
func RandomPassword(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败属于不可恢复的环境问题
		panic("homevault: 无法读取随机数: " + err.Error())
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

// TokenManager 签发并校验会话令牌。
//
// 令牌结构：base64url(payload) + "." + base64url(hmac)，其中
// payload = username \n passwordVersion \n expiryUnix。
// passwordVersion 取自用户密码哈希，因此**改密码会立刻让该用户所有会话失效**。
// 令牌是无状态的：服务端不存会话表，也不需要像同类项目那样把会话绑死在 IP+UA 上
// （那会导致用户换网络就被踢下线）。
type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenManager 用随机密钥创建令牌管理器；secret 需在进程生命周期内保持不变。
func NewTokenManager(ttl time.Duration) (*TokenManager, error) {
	if ttl <= 0 {
		return nil, errors.New("会话有效期必须大于 0")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &TokenManager{secret: secret, ttl: ttl}, nil
}

// Issue 为指定用户签发令牌，返回令牌与过期时间（使用构造时的默认有效期）。
func (m *TokenManager) Issue(username, passwordVersion string) (string, time.Time) {
	return m.IssueWithTTL(username, passwordVersion, m.ttl)
}

// IssueWithTTL 使用指定有效期签发令牌，使 sessionTTL 设置可以即时生效。
func (m *TokenManager) IssueWithTTL(username, passwordVersion string, ttl time.Duration) (string, time.Time) {
	if ttl <= 0 {
		ttl = m.ttl
	}
	exp := time.Now().Add(ttl)
	payload := strings.Join([]string{username, passwordVersion, strconv.FormatInt(exp.Unix(), 10)}, "\n")
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(enc))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return enc + "." + sig, exp
}

// ErrInvalidToken 表示令牌无效、被篡改或已过期。
var ErrInvalidToken = errors.New("会话无效或已过期")

// Verify 校验令牌并返回用户名。
func (m *TokenManager) Verify(token string) (string, error) {
	username, _, err := m.VerifySession(token)
	return username, err
}

// VerifySession 校验令牌，并同时返回令牌里携带的密码版本。
// 调用方必须把它与当前用户的 auth.PasswordVersion(user.PasswordHash) 比对，
// 不一致说明密码已被修改，该令牌必须立即失效。
func (m *TokenManager) VerifySession(token string) (username, passwordVersion string, err error) {
	parts, err := m.parse(token)
	if err != nil {
		return "", "", err
	}
	return parts[0], parts[1], nil
}

// parse 校验签名与有效期，返回 payload 的三段内容。
func (m *TokenManager) parse(token string) ([3]string, error) {
	var out [3]string
	idx := strings.LastIndex(token, ".")
	if idx <= 0 || idx == len(token)-1 {
		return out, ErrInvalidToken
	}
	enc, sig := token[:idx], token[idx+1:]
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(enc))
	wantMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return out, ErrInvalidToken
	}
	if !hmac.Equal(mac.Sum(nil), wantMAC) {
		return out, ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return out, ErrInvalidToken
	}
	parts := strings.Split(string(raw), "\n")
	if len(parts) != 3 {
		return out, ErrInvalidToken
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return out, ErrInvalidToken
	}
	if time.Now().After(time.Unix(exp, 0)) {
		return out, ErrInvalidToken
	}
	copy(out[:], parts)
	return out, nil
}

// PasswordVersion 由密码哈希派生，用于令牌失效判断。
func PasswordVersion(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:8])
}

// ---------------------------------------------------------------------------
// 登录限流：按用户名与来源 IP 分别计数，5 分钟窗口内失败 5 次即拒绝。
// ---------------------------------------------------------------------------

const (
	maxLoginFailures = 5
	loginFailWindow  = 5 * time.Minute
)

type failureRecord struct {
	count int
	until time.Time
}

// ipRecord 记录某个来源 IP 上出现过的**不同**失败账号。
// 只有当一个 IP 上出现了大量不同账号的失败尝试（撞库特征）才会封禁该 IP，
// 避免共享出口（办公网 NAT、反代没配 trustProxy）下一个人把全公司锁住。
type ipRecord struct {
	users map[string]time.Time
	until time.Time
}

// maxDistinctUsersPerIP 是同一 IP 允许失败的不同账号数上限。
const maxDistinctUsersPerIP = 8

// Limiter 是简单的内存限流器（足以覆盖小型自托管场景）。
type Limiter struct {
	mu       sync.Mutex
	byUser   map[string]*failureRecord
	byIP     map[string]*ipRecord
	lastGC   time.Time
	window   time.Duration
	maxFails int
	now      func() time.Time
}

// NewLimiter 创建限流器。
func NewLimiter() *Limiter {
	return &Limiter{
		byUser:   make(map[string]*failureRecord),
		byIP:     make(map[string]*ipRecord),
		window:   loginFailWindow,
		maxFails: maxLoginFailures,
		now:      time.Now,
	}
}

func (l *Limiter) gcLocked() {
	now := l.now()
	if now.Sub(l.lastGC) < l.window {
		return
	}
	for k, v := range l.byUser {
		if now.After(v.until) {
			delete(l.byUser, k)
		}
	}
	for k, v := range l.byIP {
		if now.After(v.until) {
			delete(l.byIP, k)
			continue
		}
		for user, exp := range v.users {
			if now.After(exp) {
				delete(v.users, user)
			}
		}
	}
	l.lastGC = now
}

// Allowed 报告给定账号当前是否允许尝试登录。
// 账号维度：同一账号 5 分钟内失败 5 次即锁定；
// IP 维度：同一 IP 上出现 maxDistinctUsersPerIP 个不同账号的失败才锁定（撞库特征），
// 因此不会因为某个同事输错密码而连坐其他人。
func (l *Limiter) Allowed(username, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()
	now := l.now()
	if r, ok := l.byUser[strings.ToLower(username)]; ok && r.count >= l.maxFails && now.Before(r.until) {
		return false
	}
	if r, ok := l.byIP[ip]; ok && now.Before(r.until) && len(r.users) >= maxDistinctUsersPerIP {
		return false
	}
	return true
}

// Fail 记录一次失败。
func (l *Limiter) Fail(username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	user := strings.ToLower(username)
	r, ok := l.byUser[user]
	if !ok || now.After(r.until) {
		r = &failureRecord{}
		l.byUser[user] = r
	}
	r.count++
	r.until = now.Add(l.window)

	rec, ok := l.byIP[ip]
	if !ok || now.After(rec.until) {
		rec = &ipRecord{users: map[string]time.Time{}}
		l.byIP[ip] = rec
	}
	rec.until = now.Add(l.window)
	rec.users[user] = now.Add(l.window)
}

// Reset 在登录成功后清除该账号的失败计数（同时把它从 IP 的失败账号集合里摘掉）。
func (l *Limiter) Reset(username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	user := strings.ToLower(username)
	delete(l.byUser, user)
	if rec, ok := l.byIP[ip]; ok {
		delete(rec.users, user)
		if len(rec.users) == 0 {
			delete(l.byIP, ip)
		}
	}
}

// RetryAfter 返回距离可以再次尝试的剩余时间（仅在 Allowed 为 false 时有意义）。
func (l *Limiter) RetryAfter(username, ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var best time.Duration
	if r, ok := l.byUser[strings.ToLower(username)]; ok && r.count >= l.maxFails && now.Before(r.until) {
		best = r.until.Sub(now)
	}
	if r, ok := l.byIP[ip]; ok && now.Before(r.until) && len(r.users) >= maxDistinctUsersPerIP {
		if d := r.until.Sub(now); d > best {
			best = d
		}
	}
	return best
}
