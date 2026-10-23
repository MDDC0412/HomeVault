package server_test

import (
	"net/http"
	"strings"
	"testing"
)

// 预览类接口会被前端用 <iframe> 内嵌，因此必须返回 X-Frame-Options: SAMEORIGIN
// （以及 CSP 的 frame-ancestors 'self'）。曾经这里对所有响应统一发 DENY，
// 导致 Excel/Word/PPT、PDF、图片预览在浏览器里全部显示"拒绝连接"。
// 本测试锁死这个行为，防止以后又被改回全局 DENY。
func TestPreviewPathsAllowSameOriginFraming(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")
	mkAlice(e)

	// 这三个路径对应前端 ui.js 里所有 el('iframe', ...) 的 src 来源。
	paths := []string{
		"/api/preview?path=a.png",
		"/api/preview/office?path=a.xlsx",
		"/api/fs/download?path=a.pdf&inline=1",
	}
	for _, p := range paths {
		r := e.do("GET", p, nil, nil)
		if got := r.header.Get("X-Frame-Options"); !strings.EqualFold(got, "SAMEORIGIN") {
			t.Errorf("%s 的 X-Frame-Options = %q，iframe 预览要求 SAMEORIGIN", p, got)
		}
		csp := r.header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("%s 的 CSP 缺少 frame-ancestors 'self'：%q", p, csp)
		}
		if strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s 的 CSP 仍然是 frame-ancestors 'none'，iframe 会被拒绝：%q", p, csp)
		}
	}
}

// mkAlice 用管理员身份建一个普通用户，供需要登录的用例复用。
func mkAlice(e *env) {
	e.mustJSON("POST", "/api/users", map[string]any{
		"username": "alice", "password": "alice12345", "root": "alice",
		"quota": 10 << 30, "rateLimitMbps": 0, "adminPassword": "admin",
	}, http.StatusOK)
	e.logout()
	e.loginAs("alice", "alice12345")
}

// 非预览路径必须保持禁止套框，避免点击劫持。
func TestNonPreviewPathsDenyFraming(t *testing.T) {
	e := newEnv(t)

	paths := []string{"/", "/admin", "/api/system", "/api/healthz"}
	for _, p := range paths {
		r := e.do("GET", p, nil, nil)
		if got := r.header.Get("X-Frame-Options"); !strings.EqualFold(got, "DENY") {
			t.Errorf("%s 的 X-Frame-Options = %q，非预览页面应为 DENY", p, got)
		}
		csp := r.header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s 的 CSP 应为 frame-ancestors 'none'：%q", p, csp)
		}
	}
}

// 安全头在预览接口上也不能丢：只是放行同源嵌入，其余防护照旧。
func TestPreviewPathsKeepOtherSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")
	mkAlice(e)

	r := e.do("GET", "/api/preview/office?path=a.xlsx", nil, nil)
	if got := r.header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("预览接口缺少 X-Content-Type-Options: nosniff，实际 %q", got)
	}
	if got := r.header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("预览接口缺少 Referrer-Policy: no-referrer，实际 %q", got)
	}
	csp := r.header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "object-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("预览接口 CSP 缺少 %q：%q", want, csp)
		}
	}
}
