package audit_test

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homevault/internal/audit"
)

func newLogger(t *testing.T) (*audit.Logger, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "audit")
	l, err := audit.New(dir)
	if err != nil {
		t.Fatalf("audit.New 失败：%v", err)
	}
	return l, dir
}

func mustLog(t *testing.T, l *audit.Logger, e audit.Entry) {
	t.Helper()
	if err := l.Log(e); err != nil {
		t.Fatalf("Log 失败：%v", err)
	}
}

func day(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

func TestLogFileNameAndDefaults(t *testing.T) {
	l, dir := newLogger(t)
	ts := time.Date(2024, 2, 29, 23, 59, 59, 0, time.UTC)
	mustLog(t, l, audit.Entry{TS: ts, User: "alice", Action: audit.ActionUpload})

	raw, err := os.ReadFile(filepath.Join(dir, "audit-2024-02-29.jsonl"))
	if err != nil {
		t.Fatalf("日志文件名不符合 audit-YYYY-MM-DD.jsonl：%v", err)
	}
	if !strings.Contains(string(raw), `"status":"ok"`) {
		t.Fatalf("Status 为空时应默认 ok：%s", raw)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("每条记录应以换行结尾：%q", raw)
	}

	// TS 零值 → 当天 + UTC
	mustLog(t, l, audit.Entry{User: "bob", Action: audit.ActionLogin})
	today := time.Now().UTC().Format("2006-01-02")
	if _, err := os.Stat(filepath.Join(dir, "audit-"+today+".jsonl")); err != nil {
		t.Fatalf("TS 零值应写入当天文件：%v", err)
	}
	res, err := l.Query(audit.Query{User: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].TS.Location() != time.UTC {
		t.Fatalf("TS 应转为 UTC：%+v", res.Entries)
	}
}

func TestQueryFiltersAndOrder(t *testing.T) {
	l, _ := newLogger(t)
	// 3 天、4 个用户、多种动作
	mustLog(t, l, audit.Entry{TS: day(2024, 3, 1, 8), User: "alice", Action: audit.ActionUpload, Target: "docs/a.pdf", Bytes: 100})
	mustLog(t, l, audit.Entry{TS: day(2024, 3, 1, 9), User: "alice", Action: audit.ActionDownload, Target: "docs/a.pdf", Bytes: 100})
	mustLog(t, l, audit.Entry{TS: day(2024, 3, 2, 10), User: "bob", Action: audit.ActionUpload, Target: "b.txt", Message: "分片上传"})
	mustLog(t, l, audit.Entry{TS: day(2024, 3, 2, 11), User: "alice", Action: audit.ActionDelete, Target: "a.pdf", Status: audit.StatusDenied})
	mustLog(t, l, audit.Entry{TS: day(2024, 3, 3, 12), User: "carol", Action: audit.ActionMkdir, Target: "新建目录"})

	// 全量：必须按时间倒序（最新在前）
	res, err := l.Query(audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Entries) != 5 {
		t.Fatalf("全量查询应有 5 条，实际 total=%d len=%d", res.Total, len(res.Entries))
	}
	for i := 1; i < len(res.Entries); i++ {
		if res.Entries[i].TS.After(res.Entries[i-1].TS) {
			t.Fatalf("结果未按时间倒序：%v 在 %v 之后", res.Entries[i].TS, res.Entries[i-1].TS)
		}
	}
	if res.Entries[0].User != "carol" {
		t.Fatalf("最新一条应是 carol，实际 %s", res.Entries[0].User)
	}

	cases := []struct {
		name  string
		q     audit.Query
		total int
	}{
		{"按用户", audit.Query{User: "alice"}, 3},
		{"按用户（大小写不敏感）", audit.Query{User: "ALICE"}, 3},
		{"按动作", audit.Query{Action: audit.ActionUpload}, 2},
		{"按动作（精确匹配，不认前缀）", audit.Query{Action: "up"}, 0},
		{"关键词命中 target（不区分大小写）", audit.Query{Search: "DOCS/"}, 2},
		{"关键词命中 message", audit.Query{Search: "分片"}, 1},
		{"时间下界（含边界）", audit.Query{From: day(2024, 3, 2, 10)}, 3},
		{"时间上界（含边界）", audit.Query{To: day(2024, 3, 2, 10)}, 3},
		{"时间区间", audit.Query{From: day(2024, 3, 1, 9), To: day(2024, 3, 2, 11)}, 3},
		{"用户+动作组合", audit.Query{User: "alice", Action: audit.ActionUpload}, 1},
		{"无命中", audit.Query{User: "dave"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := l.Query(c.q)
			if err != nil {
				t.Fatal(err)
			}
			if r.Total != c.total || len(r.Entries) != c.total {
				t.Fatalf("查询 %+v 应得 %d 条，实际 total=%d len=%d", c.q, c.total, r.Total, len(r.Entries))
			}
		})
	}
}

func TestQueryLimitOffsetAndTotal(t *testing.T) {
	l, _ := newLogger(t)
	for i := 0; i < 60; i++ {
		mustLog(t, l, audit.Entry{TS: day(2024, 4, 1, 0).Add(time.Duration(i) * time.Minute),
			User: "u", Action: audit.ActionSearch, Target: fmt.Sprintf("f%02d", i)})
	}
	res, err := l.Query(audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 50 {
		t.Fatalf("默认 limit 应为 50，实际 %d", len(res.Entries))
	}
	if res.Total != 60 {
		t.Fatalf("Total 应为过滤后的总数 60（不受 limit 影响），实际 %d", res.Total)
	}
	if res.Entries[0].Target != "f59" {
		t.Fatalf("第一条应是最新的 f59，实际 %s", res.Entries[0].Target)
	}

	res, err = l.Query(audit.Query{Limit: 3, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 3 || res.Entries[0].Target != "f57" || res.Entries[2].Target != "f55" {
		t.Fatalf("limit+offset 分页不正确：%+v", targets(res.Entries))
	}
	if res.Total != 60 {
		t.Fatalf("offset 不影响 Total，实际 %d", res.Total)
	}

	// limit 上限 500：造 501 条，只应返回 500
	l2, _ := newLogger(t)
	for i := 0; i < 501; i++ {
		mustLog(t, l2, audit.Entry{TS: day(2024, 5, 1, 0).Add(time.Duration(i) * time.Second), User: "u", Action: audit.ActionSearch})
	}
	res, err = l2.Query(audit.Query{Limit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 500 || res.Total != 501 {
		t.Fatalf("limit 应被截到 500：len=%d total=%d", len(res.Entries), res.Total)
	}
}

func targets(es []audit.Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Target)
	}
	return out
}

func TestQuerySkipsCorruptLinesAndCounts(t *testing.T) {
	l, dir := newLogger(t)
	good := `{"ts":"2024-06-01T10:00:00Z","user":"alice","action":"upload","status":"ok"}`
	path := filepath.Join(dir, "audit-2024-06-01.jsonl")
	content := strings.Join([]string{
		good,
		"{ 这不是 JSON",
		`{"ts":"2024-06-01T11:00:00Z","user":"bob"`, // 截断行
		"",
		`{"ts":"2024-06-01T12:00:00Z","user":"carol","action":"login","status":"ok"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := l.Query(audit.Query{})
	if err != nil {
		t.Fatalf("损坏行不应让整个查询失败：%v", err)
	}
	if res.Total != 2 {
		t.Fatalf("应只返回 2 条合法记录，实际 %d", res.Total)
	}
	if res.Entries[0].User != "carol" {
		t.Fatalf("倒序不正确：%+v", res.Entries)
	}
	if n, err := l.Count(); err != nil || n != 4 {
		t.Fatalf("Count 应按行数统计（含损坏行）为 4，实际 %d err=%v", n, err)
	}
	// 不认识的日志文件名不参与
	if err := os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := l.Count()
	if err != nil || n != 4 {
		t.Fatalf("非 audit-*.jsonl 文件不应被统计，实际 %d", n)
	}
}

func TestQueryOnMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	l, err := audit.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	res, err := l.Query(audit.Query{})
	if err != nil {
		t.Fatalf("目录不存在时应返回空结果而不是错误：%v", err)
	}
	if res.Total != 0 || len(res.Entries) != 0 {
		t.Fatalf("应为空结果：%+v", res)
	}
	if n, err := l.Count(); err != nil || n != 0 {
		t.Fatalf("Count 应为 0，实际 %d err=%v", n, err)
	}
}

func TestCleanupByRetention(t *testing.T) {
	l, dir := newLogger(t)
	// 直接注入旧文件名制造过期数据
	for _, name := range []string{"audit-2020-01-01.jsonl", "audit-2021-06-30.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"user":"old","action":"upload"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustLog(t, l, audit.Entry{TS: time.Now().UTC(), User: "new", Action: audit.ActionUpload})
	today := "audit-" + time.Now().UTC().Format("2006-01-02") + ".jsonl"

	if n, err := l.Cleanup(0); err != nil || n != 0 {
		t.Fatalf("retention=0 表示不清理，实际 n=%d err=%v", n, err)
	}
	if n, err := l.Cleanup(-time.Hour); err != nil || n != 0 {
		t.Fatalf("负保留期不应清理，实际 n=%d err=%v", n, err)
	}

	n, err := l.Cleanup(30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应删除 2 个过期文件，实际 %d", n)
	}
	for _, name := range []string{"audit-2020-01-01.jsonl", "audit-2021-06-30.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 应被删除：%v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, today)); err != nil {
		t.Fatalf("当天文件不能被删除：%v", err)
	}

	// 短保留期也不应删掉当天文件（文件日期的次日仍晚于截止时间）
	n, err = l.Cleanup(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("刚写入的日志不应被清理，实际删了 %d 个", n)
	}
}

func TestWriteCSVHasBOMAndEscapes(t *testing.T) {
	es := []audit.Entry{
		{
			TS: time.Date(2024, 7, 1, 8, 9, 10, 0, time.Local), User: "alice", Action: audit.ActionUpload,
			Target: `a,b"c.txt`, Status: audit.StatusOK, Bytes: 1234, IP: "10.0.0.1",
			UserAgent: "Mozilla/5.0 \"X\"", Message: "第一行\n第二行",
		},
		{
			TS: time.Date(2024, 7, 1, 9, 0, 0, 0, time.Local), User: "bob", Action: audit.ActionDelete,
			Target: "x", Status: audit.StatusDenied, Bytes: 0,
		},
	}
	var buf bytes.Buffer
	if err := audit.WriteCSV(&buf, es); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Fatalf("CSV 必须以 UTF-8 BOM 开头，实际 % x", raw[:3])
	}
	body := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("生成的 CSV 不能被 encoding/csv 解析：%v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("应有 1 行表头 + 2 行数据，实际 %d", len(rows))
	}
	wantHeader := []string{"时间", "用户", "动作", "目标", "状态", "字节", "IP", "客户端", "说明"}
	for i, h := range wantHeader {
		if rows[0][i] != h {
			t.Fatalf("表头第 %d 列应为 %q，实际 %q", i, h, rows[0][i])
		}
	}
	if rows[1][3] != `a,b"c.txt` {
		t.Fatalf("含逗号/引号的目标字段转义错误：%q", rows[1][3])
	}
	if rows[1][8] != "第一行\n第二行" {
		t.Fatalf("含换行的说明字段转义错误：%q", rows[1][8])
	}
	if rows[1][5] != "1234" {
		t.Fatalf("字节应为十进制字符串：%q", rows[1][5])
	}
	if rows[1][0] != "2024-07-01 08:09:10" {
		t.Fatalf("时间格式应为 2006-01-02 15:04:05（本地时间）：%q", rows[1][0])
	}
}

// audit 包自身不做"调用者隔离"：Query 只按显式传入的 User 过滤。
// HTTP 层（server.handleLogs）负责给非管理员强制 q.User = 当前账号。
func TestQueryUserFilterIsCallerResponsibility(t *testing.T) {
	l, _ := newLogger(t)
	mustLog(t, l, audit.Entry{TS: day(2024, 8, 1, 1), User: "alice", Action: audit.ActionUpload, Target: "a.txt"})
	mustLog(t, l, audit.Entry{TS: day(2024, 8, 1, 2), User: "bob", Action: audit.ActionUpload, Target: "b.txt"})

	res, err := l.Query(audit.Query{User: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Entries[0].User != "bob" {
		t.Fatalf("按 User 过滤应只剩 bob 的记录：%+v", res.Entries)
	}
	// 不传 User 时会看到所有人的记录 —— 这是包级约定，不是漏洞，但调用方必须自己加限制
	res, err = l.Query(audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("不传 User 应能看到全部 2 条（调用方需自行限制）：%d", res.Total)
	}
	// Action 过滤不区分大小写这件事并不成立：动作是精确匹配
	res, err = l.Query(audit.Query{Action: "UPLOAD"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Fatalf("Action 是精确匹配（区分大小写），实际命中 %d 条", res.Total)
	}
}
