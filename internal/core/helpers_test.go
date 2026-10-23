package core

import (
	"path/filepath"
	"testing"
)

func TestJoinRelAndParentRel(t *testing.T) {
	join := []struct {
		dir, name, want string
	}{
		{"", "a", "a"},
		{"d", "a", "d/a"},
		{"a/b", "c", "a/b/c"},
	}
	for _, c := range join {
		if got := joinRel(c.dir, c.name); got != c.want {
			t.Fatalf("joinRel(%q,%q)=%q，期望 %q", c.dir, c.name, got, c.want)
		}
	}
	parent := []struct {
		in, want string
	}{
		{"a", ""},
		{"a/b", "a"},
		{"a/b/c", "a/b"},
		{"", ""},
		{"/a", ""},
	}
	for _, c := range parent {
		if got := parentRel(c.in); got != c.want {
			t.Fatalf("parentRel(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 99: "99", 12345: "12345", -5: "-5", -100: "-100"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Fatalf("itoa(%d)=%q，期望 %q", in, got, want)
		}
	}
}

func TestNewIDIsUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := newID("t_")
		if len(id) != 2+18 {
			t.Fatalf("id 长度应为前缀+18 个十六进制字符，实际 %q", id)
		}
		if id[:2] != "t_" {
			t.Fatalf("id 前缀错误：%q", id)
		}
		if seen[id] {
			t.Fatalf("id 重复：%q", id)
		}
		seen[id] = true
	}
}

// evalPath 规范化路径：Windows 上 t.TempDir() 可能给出 8.3 短名（如 DISILL~1），
// 而 fsops.Resolve 内部走 EvalSymlinks 得到长名，两边直接比对会误判"越界"。
// 比较路径时统一过这个函数，让测试只验证真实的安全语义。
func evalPath(p string) string {
	if ev, err := filepath.EvalSymlinks(p); err == nil {
		return ev
	}
	return p
}
