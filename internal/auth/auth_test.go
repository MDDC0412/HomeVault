package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// regression：这些用例原本用于复现已知缺陷；缺陷修复后改为默认运行的回归用例，id/desc 只用于回溯。
func regression(t *testing.T, id, desc string) {
	t.Helper()
	// 该缺陷已修复，用例默认运行；id/desc 仅用于回溯历史。
	t.Logf("回归用例 %s：%s", id, desc)
}

func testBoolEnv(k string) bool { return os.Getenv(k) == "1" }

func TestHashPasswordFormatAndVerify(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("空密码必须报错")
	}
	const pw = "Correct-Horse-9"
	enc, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(enc, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		t.Fatalf("哈希格式应为 pbkdf2-sha256$iters$salt$hash，实际 %q", enc)
	}
	if parts[1] != "210000" {
		t.Fatalf("迭代次数应为 210000，实际 %q", parts[1])
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) != 16 {
		t.Fatalf("salt 应为 16 字节 RawStdEncoding：%d %v", len(salt), err)
	}
	sum, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(sum) != 32 {
		t.Fatalf("hash 应为 32 字节 RawStdEncoding：%d %v", len(sum), err)
	}
	if !VerifyPassword(enc, pw) {
		t.Fatal("正确密码必须通过")
	}
	if VerifyPassword(enc, pw+"x") || VerifyPassword(enc, "") {
		t.Fatal("错误密码必须失败")
	}
	if VerifyPassword(enc, strings.ToUpper(pw)) {
		t.Fatal("密码必须区分大小写")
	}

	// 同一密码两次哈希：盐不同 → 密文不同，但都能验证
	enc2, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if enc == enc2 {
		t.Fatal("同一密码两次哈希不应相同（盐必须随机）")
	}
	if !VerifyPassword(enc2, pw) {
		t.Fatal("第二次哈希也应能验证")
	}
}

func TestVerifyPasswordRejectsTamperedInput(t *testing.T) {
	enc, err := HashPassword("pw-123456")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(enc, "$")
	bad := []string{
		"",
		"plain",
		"pbkdf2-sha256$210000$onlythree",
		"bcrypt$210000$AA$BB",
		"pbkdf2-sha256$abc$AA$BB",
		"pbkdf2-sha256$0$AA$BB",
		"pbkdf2-sha256$-1$AA$BB",
		"pbkdf2-sha256$5000001$AA$BB",
		"pbkdf2-sha256$210000$!!$" + parts[3],
		"pbkdf2-sha256$210000$" + parts[2] + "$!!",
		"pbkdf2-sha256$210001$" + parts[2] + "$" + parts[3], // 迭代次数被改
		"pbkdf2-sha256$210000$" + parts[2] + "$" + flipHead(parts[3]),
		"pbkdf2-sha256$210000$" + parts[2] + "$" + flipMid(parts[3]),
		"pbkdf2-sha256$210000$" + flipHead(parts[2]) + "$" + parts[3], // 盐被改
		" " + enc,
	}
	for _, e := range bad {
		if VerifyPassword(e, "pw-123456") {
			t.Fatalf("被篡改的哈希 %q 不应通过", e)
		}
	}
}

// VerifyPassword 完全信任编码里的 iters/salt 长度：这是为了兼容未来参数升级，
// 但也意味着一个极弱的哈希（iters=1、1 字节摘要）会被接受。
func TestVerifyPasswordHonorsWeakEmbeddedParams(t *testing.T) {
	salt := []byte{0x01}
	sum, err := pbkdf2.Key(sha256.New, "weak", salt, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	enc := "pbkdf2-sha256$1$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(sum)
	if !VerifyPassword(enc, "weak") {
		t.Fatal("当前实现会接受 iters=1 的弱哈希")
	}
	if VerifyPassword(enc, "strong") {
		t.Fatal("弱哈希仍必须校验密码本身")
	}
}

// PBKDF2 的输出是"前缀稳定"的：VerifyPassword 用 len(want) 作为派生长度，
// 所以把保存的哈希截短（例如 state.json 被手工改坏）不会报错，只会削弱强度。
func TestVerifyPasswordAcceptsTruncatedDigest(t *testing.T) {
	enc, err := HashPassword("pw-123456")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(enc, "$")
	trunc := "pbkdf2-sha256$210000$" + parts[2] + "$" + parts[3][:len(parts[3])-4]
	if !VerifyPassword(trunc, "pw-123456") {
		t.Fatal("当前实现会接受被截短的哈希（PBKDF2 前缀稳定）")
	}
	if VerifyPassword(trunc, "wrong-password") {
		t.Fatal("截短的哈希仍必须校验密码本身")
	}
	// 但只要动了前 29 字节之外的任何有效位就应当失败
	if VerifyPassword("pbkdf2-sha256$210000$"+parts[2]+"$"+flipHead(parts[3]), "pw-123456") {
		t.Fatal("哈希被改动后必须失败")
	}
}

func TestPasswordVersion(t *testing.T) {
	a := PasswordVersion("hash-a")
	b := PasswordVersion("hash-b")
	if a == b {
		t.Fatal("不同哈希必须派生出不同版本")
	}
	if a != PasswordVersion("hash-a") {
		t.Fatal("PasswordVersion 必须确定性")
	}
	if len(a) != 16 {
		t.Fatalf("版本应为 16 个十六进制字符（8 字节），实际 %q", a)
	}
	if PasswordVersion("") == "" {
		t.Fatal("空哈希也应得到一个（非空）版本值")
	}
}

func TestRandomPassword(t *testing.T) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for _, n := range []int{0, 1, 8, 20, 64} {
		p := RandomPassword(n)
		if len(p) != n {
			t.Fatalf("RandomPassword(%d) 长度 %d", n, len(p))
		}
		for _, r := range p {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("RandomPassword 出现了字符集外的字符 %q", r)
			}
			if strings.ContainsRune("01lIoO", r) {
				t.Fatalf("字母表不应包含易混淆字符 %q", r)
			}
		}
	}
	if RandomPassword(32) == RandomPassword(32) {
		t.Fatal("两次生成的密码不应相同")
	}
}

func TestTokenIssueAndVerify(t *testing.T) {
	m, err := NewTokenManager(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	tok, exp := m.Issue("alice", "v1")
	after := time.Now()
	if len(strings.Split(tok, ".")) != 2 {
		t.Fatalf("令牌应为 payload.sig 两段：%q", tok)
	}
	if exp.Before(before.Add(time.Hour-time.Minute)) || exp.After(after.Add(time.Hour+time.Minute)) {
		t.Fatalf("过期时间应约为 1 小时后：%v", exp)
	}
	got, err := m.Verify(tok)
	if err != nil {
		t.Fatalf("Verify 失败：%v", err)
	}
	if got != "alice" {
		t.Fatalf("Verify 应返回用户名 alice，实际 %q", got)
	}
	// IssueWithTTL 的 ttl<=0 回落到构造时的默认值
	for _, ttl := range []time.Duration{0, -time.Second} {
		_, exp := m.IssueWithTTL("bob", "v1", ttl)
		if d := time.Until(exp); d < 50*time.Minute || d > 70*time.Minute {
			t.Fatalf("ttl=%v 应回落默认 1h，实际 %v", ttl, d)
		}
	}
	// 不同密钥的管理器互不认可
	m2, err := NewTokenManager(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Verify(tok); err == nil {
		t.Fatal("换密钥后旧令牌必须失效")
	}
}

func TestNewTokenManagerRejectsNonPositiveTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Minute} {
		if _, err := NewTokenManager(ttl); err == nil {
			t.Fatalf("ttl=%v 必须报错", ttl)
		}
	}
}

func TestTokenTamperAndExpiry(t *testing.T) {
	m, err := NewTokenManager(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := m.Issue("alice", "v1")
	idx := strings.LastIndex(tok, ".")
	enc, sig := tok[:idx], tok[idx+1:]

	// 另签一个令牌，用来拿到"格式合法但 MAC 不匹配"的签名
	other, _ := m.Issue("bob", "v1")
	oIdx := strings.LastIndex(other, ".")
	otherEnc, otherSig := other[:oIdx], other[oIdx+1:]

	// 把 payload 换成另一个用户的（签名不动）
	otherPayload := base64.RawURLEncoding.EncodeToString([]byte("bob\nv1\n" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)))

	bad := []string{
		"",
		".",
		".abc",
		"abc.",
		"nodot",
		enc,                       // 缺签名
		enc + ".",                 // 空签名
		sig + "." + enc,           // 段顺序颠倒
		enc + "." + sig + "x",     // 签名被追加
		enc + ".!" + sig[1:],      // 签名含非法 base64
		otherPayload + "." + sig,  // payload 被换、MAC 不匹配
		enc + "." + otherSig,      // 签名被换成别人的
		otherEnc + "." + otherEnc, // 用 payload 冒充签名
		enc + "." + flipMid(sig),  // 中间字符被改（一定改变 MAC）
	}
	for _, b := range bad {
		if _, err := m.Verify(b); err == nil {
			t.Fatalf("非法令牌 %q 不应通过", b)
		}
	}

	// 过期：用同一套算法手工构造到期时间，避免依赖 sleep（测试必须确定性）
	expired := mintToken(t, m, "bob", "v1", time.Now().Add(-time.Second))
	if _, err := m.Verify(expired); err == nil {
		t.Fatal("过期令牌必须被拒绝")
	}
	future := mintToken(t, m, "bob", "v1", time.Now().Add(time.Hour))
	if name, err := m.Verify(future); err != nil || name != "bob" {
		t.Fatalf("未过期令牌应通过：%v %q", err, name)
	}
	// IssueWithTTL 必须真的使用传入的 TTL（≥1s 才一定有效，见下面的秒级精度说明）
	shortTok, exp := m.IssueWithTTL("bob", "v1", 3*time.Second)
	if d := time.Until(exp); d <= time.Second || d > 5*time.Second {
		t.Fatalf("自定义 TTL 未生效：%v", d)
	}
	if _, err := m.Verify(shortTok); err != nil {
		t.Fatalf("刚签发的短 TTL 令牌应通过：%v", err)
	}
	// IssueWithTTL 把到期时间按整秒写入 payload（expiryUnix），因此 TTL 不足 1 秒时
	// 令牌"是否立刻过期"取决于签发时刻在秒内的位置。这里只断言确实按整秒截断，
	// 不对这种边界做时序断言（否则测试会因为运行时刻不同而随机失败）。
	base := time.Now()
	_, exp500 := m.IssueWithTTL("bob", "v1", 500*time.Millisecond)
	if exp500.Unix() != base.Add(500*time.Millisecond).Unix() {
		t.Fatalf("TTL 的到期时间没有按传入值计算：%v", exp500)
	}
	// Verify 用 time.Now().After(time.Unix(exp,0)) 判断，即"进入 exp 那一秒就算过期"：
	// 所以 exp 等于当前秒的令牌立刻失效（比 TTL 名义上早最多 1 秒）。
	nowTok := mintToken(t, m, "bob", "v1", time.Unix(time.Now().Unix(), 0))
	if _, err := m.Verify(nowTok); err == nil {
		t.Fatal("exp 等于当前秒的令牌必须失效（到秒即过期）")
	}
	// TTL=1s 时 exp 至少是下一秒，因此一定有效
	oneTok, _ := m.IssueWithTTL("bob", "v1", time.Second)
	if _, err := m.Verify(oneTok); err != nil {
		t.Fatalf("TTL=1s 的令牌应有效：%v", err)
	}
	// 往回 2 秒必然跨过到期秒，一定失效
	justExpired := mintToken(t, m, "bob", "v1", time.Now().Add(-2*time.Second))
	if _, err := m.Verify(justExpired); err == nil {
		t.Fatal("已跨过到期秒的令牌必须被拒绝")
	}
}

// VerifyPassword 会用 base64 解码器跳过 \r\n，因此哈希串带尾随换行仍能通过。
func TestVerifyPasswordToleratesTrailingNewline(t *testing.T) {
	enc, err := HashPassword("pw-123456")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(enc+"\n", "pw-123456") {
		t.Fatal("当前实现忽略 base64 输入里的换行（标准库行为）")
	}
	if !VerifyPassword(enc+"\r\n", "pw-123456") {
		t.Fatal("当前实现忽略 base64 输入里的 CRLF（标准库行为）")
	}
	// 但前缀加东西会破坏第一部分，必须失败
	if VerifyPassword("\n"+enc, "pw-123456") {
		t.Fatal("前缀污染必须导致失败")
	}
}

// mintToken 手工按 payload=username\nversion\nexpiry 的格式签名（白盒：直接用 m.secret）。
func mintToken(t *testing.T, m *TokenManager, username, version string, exp time.Time) string {
	t.Helper()
	payload := strings.Join([]string{username, version, strconv.FormatInt(exp.Unix(), 10)}, "\n")
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// flipHead 改变字符串首字符（RawStdEncoding 尾字符的低位会被忽略，改首字符一定改变解码结果）。
func flipHead(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

// flipMid 改变字符串中间的字符（避开 base64 尾部被忽略的低位）。
func flipMid(s string) string {
	b := []byte(s)
	i := len(b) / 2
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}

// Verify 只返回用户名，拿不到 payload 里的 passwordVersion，
// 因此“改密码立刻踢下线”这件事必须由调用方（HTTP 层）自己比对。
func TestVerifyIgnoresPasswordVersion(t *testing.T) {
	m, err := NewTokenManager(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v1 := PasswordVersion("old-hash")
	v2 := PasswordVersion("new-hash")
	if v1 == v2 {
		t.Fatal("前提不成立：两个版本应不同")
	}
	tok, _ := m.Issue("alice", v1)
	name, err := m.Verify(tok)
	if err != nil || name != "alice" {
		t.Fatalf("Verify 应通过：%v %q", err, name)
	}
	// 即使密码版本已经变成 v2，Verify 依然认可旧令牌 —— 这正是 server 层必须补的比对。
	if _, err := m.Verify(tok); err != nil {
		t.Fatalf("当前实现 Verify 不做版本比对：%v", err)
	}
}

// 用户名里带换行会破坏 payload 结构；当前实现会直接判为非法（fail-closed）。
func TestTokenRejectsUsernameWithNewline(t *testing.T) {
	m, err := NewTokenManager(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := m.Issue("eve\nalice", "v1")
	if _, err := m.Verify(tok); err == nil {
		t.Fatal("含换行的用户名应让令牌解析失败，而不是解析出伪造的用户名")
	}
}

// ---------------------------------------------------------------- Limiter

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newLimiterAt(clk *fakeClock) *Limiter {
	l := NewLimiter()
	l.now = clk.now
	return l
}

// 限流契约（D7 修复后）：
//   - 账号维度：同一账号 5 分钟内失败 maxLoginFailures 次即锁定，换 IP 也锁；
//   - IP 维度：只有同一个 IP 上出现 maxDistinctUsersPerIP 个**不同**账号的失败
//     （撞库特征）才锁定该 IP，不再因为某个同事输错密码而连带锁定整个出口。
func TestLimiterLocksPerUserAndOnlyForCredentialStuffingPerIP(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	l := newLimiterAt(clk)
	const user, ip = "alice", "10.0.0.1"

	for i := 1; i <= maxLoginFailures; i++ {
		if !l.Allowed(user, ip) {
			t.Fatalf("第 %d 次尝试前不应被拦截", i)
		}
		l.Fail(user, ip)
	}
	// (a) 账号维度：换 IP、换大小写照样锁
	if l.Allowed(user, ip) {
		t.Fatalf("%d 次失败后必须拦截", maxLoginFailures)
	}
	if l.Allowed("ALICE", "10.0.0.2") {
		t.Fatal("按账号限流必须大小写不敏感，且不限于来源 IP")
	}
	if d := l.RetryAfter(user, "10.0.0.2"); d <= 0 || d > loginFailWindow {
		t.Fatalf("RetryAfter 应在 (0, %v] 内，实际 %v", loginFailWindow, d)
	}
	// (b) 同一 IP 上的其它账号不受连坐
	if !l.Allowed("bob", ip) {
		t.Fatal("同一 IP 上的其它账号不应被连带锁定")
	}
	l.Reset("bob", ip)
	if l.Allowed(user, ip) {
		t.Fatal("Reset 其它账号不应解除本人的锁定")
	}

	// (c) 撞库特征：同一 IP 上 maxDistinctUsersPerIP 个不同账号都失败过 → 锁 IP
	l2 := newLimiterAt(clk)
	for i := 0; i < maxDistinctUsersPerIP; i++ {
		u := "victim-" + string(rune('a'+i))
		if !l2.Allowed(u, ip) {
			t.Fatalf("第 %d 个不同账号失败前不应锁定该 IP", i+1)
		}
		l2.Fail(u, ip)
	}
	if l2.Allowed("fresh", ip) {
		t.Fatalf("同一 IP 上 %d 个不同账号失败后应锁定该 IP", maxDistinctUsersPerIP)
	}
	if d := l2.RetryAfter("fresh", ip); d <= 0 {
		t.Fatalf("IP 锁定时 RetryAfter 应为正，实际 %v", d)
	}
	if !l2.Allowed("fresh", "10.0.0.3") {
		t.Fatal("IP 维度锁定不应影响其它来源 IP")
	}
	clk.add(loginFailWindow + time.Second)
	if !l2.Allowed("fresh", ip) {
		t.Fatal("窗口过后必须解除 IP 锁定")
	}
}

func TestLimiterWindowExpiry(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	l := newLimiterAt(clk)
	const user, ip = "alice", "10.0.0.1"
	for i := 0; i < maxLoginFailures; i++ {
		l.Fail(user, ip)
	}
	clk.add(loginFailWindow - time.Second)
	if l.Allowed(user, ip) {
		t.Fatal("窗口内必须继续拦截")
	}
	if d := l.RetryAfter(user, ip); d > time.Second+time.Millisecond {
		t.Fatalf("RetryAfter 应随窗口临近而减少，实际 %v", d)
	}
	clk.add(2 * time.Second)
	if !l.Allowed(user, ip) {
		t.Fatal("窗口过后必须解除拦截")
	}
	if d := l.RetryAfter(user, ip); d != 0 {
		t.Fatalf("解除后 RetryAfter 应为 0，实际 %v", d)
	}
	// 窗口过后重新计数：再失败 4 次仍应放行
	for i := 0; i < maxLoginFailures-1; i++ {
		l.Fail(user, ip)
	}
	if !l.Allowed(user, ip) {
		t.Fatal("窗口重置后计数应从 0 重新开始")
	}
}

func TestLimiterResetOnSuccess(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	l := newLimiterAt(clk)
	const user, ip = "alice", "10.0.0.1"
	for i := 0; i < maxLoginFailures; i++ {
		l.Fail(user, ip)
	}
	if l.Allowed(user, ip) {
		t.Fatal("前提：应已被拦截")
	}
	l.Reset(user, ip)
	if !l.Allowed(user, ip) {
		t.Fatal("登录成功后必须清零计数")
	}
	if d := l.RetryAfter(user, ip); d != 0 {
		t.Fatalf("清零后 RetryAfter 应为 0，实际 %v", d)
	}
}

func TestLimiterGC(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	l := newLimiterAt(clk)
	for i := 0; i < maxLoginFailures; i++ {
		l.Fail("alice", "10.0.0.1")
	}
	clk.add(loginFailWindow + time.Second)
	l.Allowed("alice", "10.0.0.1") // 触发 gcLocked
	if n := len(l.byUser); n != 0 {
		t.Fatalf("过期记录应被回收，byUser 仍有 %d 条", n)
	}
	if n := len(l.byIP); n != 0 {
		t.Fatalf("过期记录应被回收，byIP 仍有 %d 条", n)
	}
}

func TestLimiterConcurrentAccess(t *testing.T) {
	l := NewLimiter()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l.Allowed("alice", "10.0.0.1")
				l.Fail("alice", "10.0.0.1")
				l.RetryAfter("alice", "10.0.0.1")
				if j%10 == 0 {
					l.Reset("alice", "10.0.0.1")
				}
			}
		}(i)
	}
	wg.Wait()
}
