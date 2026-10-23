// Package audit 实现审计日志：按天一个 JSONL 文件，读取时按时间倒序合并。
//
// 立场：审计是"必须能查到谁在什么时候动了什么"，因此
//   - 写入失败绝不影响主流程（best-effort，错误回传给调用方记录到服务端日志）；
//   - 读取支持分页、按用户/动作/关键词/时间范围过滤，并支持导出 CSV；
//   - 保留期由运行期设置决定，0 表示永不自动清理。
package audit

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 动作枚举。
const (
	ActionLogin       = "login"
	ActionLoginFailed = "login_failed"
	ActionLogout      = "logout"
	ActionUpload      = "upload"
	ActionDownload    = "download"
	ActionMkdir       = "mkdir"
	ActionRename      = "rename"
	ActionMove        = "move"
	ActionCopy        = "copy"
	ActionDelete      = "delete"
	ActionRestore     = "restore"
	ActionPurge       = "purge"
	ActionTrashEmpty  = "trash_empty"
	ActionUserCreate  = "user_create"
	ActionUserUpdate  = "user_update"
	ActionUserDelete  = "user_delete"
	ActionConfigSet   = "config_reload"
	ActionCleanup     = "cleanup"
	ActionSearch      = "search"
	ActionPreview     = "preview"
	ActionReauth      = "reauth"
	ActionAdminAccess = "admin_access"
	ActionSettingsSet = "settings_set"
	ActionTextSave    = "text_save"
	// ActionLogsPurge 记录"清除历史日志"这一动作本身（会写进清理后的新日志里）。
	ActionLogsPurge = "logs_purge"
)

// 状态枚举。
const (
	StatusOK     = "ok"
	StatusDenied = "denied"
	StatusError  = "error"
)

// Entry 是一条审计记录。
type Entry struct {
	TS time.Time `json:"ts"`
	// ActingAs 仅在管理员代为操作用户文件时出现，记录被操作的账号。
	ActingAs  string `json:"actingAs,omitempty"`
	User      string `json:"user"`
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"ua,omitempty"`
	Action    string `json:"action"`
	Target    string `json:"target,omitempty"`
	Status    string `json:"status"`
	Bytes     int64  `json:"bytes,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Logger 负责审计日志的写入、查询与清理。
type Logger struct {
	dir string
	mu  sync.Mutex
}

// New 创建日志器并确保目录存在。
func New(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Logger{dir: dir}, nil
}

const filePrefix = "audit-"
const fileSuffix = ".jsonl"

func fileName(t time.Time) string {
	return filePrefix + t.Format("2006-01-02") + fileSuffix
}

// Log 追加一条记录。
func (l *Logger) Log(e Entry) error {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	} else {
		e.TS = e.TS.UTC()
	}
	if e.Status == "" {
		e.Status = StatusOK
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(l.dir, fileName(e.TS)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// Query 描述一次日志查询。
type Query struct {
	Limit  int    // 默认 50，上限 500
	Offset int    // 跳过条数
	User   string // 精确匹配（不区分大小写）
	Action string // 精确匹配
	Search string // 对 target / message 做不区分大小写的子串匹配
	From   time.Time
	To     time.Time
}

// Result 是查询结果。
type Result struct {
	Entries []Entry
	Total   int
}

// files 返回按日期倒序排列的日志文件。
func (l *Logger) files() ([]string, error) {
	items, err := os.ReadDir(l.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, it := range items {
		name := it.Name()
		if !it.IsDir() && strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix) {
			names = append(names, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Join(l.dir, n))
	}
	return out, nil
}

// Query 执行查询。返回的 Entries 按时间倒序（最新在前）。
func (l *Logger) Query(q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	files, err := l.files()
	if err != nil {
		return Result{}, err
	}
	res := Result{Entries: []Entry{}}
	skipped := 0
	for _, path := range files {
		// 快速跳过：文件日期早于 From 的整天
		if !q.From.IsZero() {
			if d, ok := dateOf(path); ok && d.Add(24*time.Hour).Before(q.From) {
				continue
			}
		}
		lines, err := readLines(path)
		if err != nil {
			return res, err
		}
		for i := len(lines) - 1; i >= 0; i-- {
			var e Entry
			if err := json.Unmarshal(lines[i], &e); err != nil {
				continue // 损坏行跳过，不让一行脏数据炸掉整个日志页
			}
			if !match(e, q) {
				continue
			}
			res.Total++
			if skipped < q.Offset {
				skipped++
				continue
			}
			if len(res.Entries) < q.Limit {
				res.Entries = append(res.Entries, e)
			}
		}
	}
	return res, nil
}

func dateOf(path string) (time.Time, bool) {
	name := filepath.Base(path)
	name = strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
	d, err := time.ParseInLocation("2006-01-02", name, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

func match(e Entry, q Query) bool {
	if q.User != "" && !strings.EqualFold(e.User, q.User) {
		return false
	}
	if q.Action != "" && e.Action != q.Action {
		return false
	}
	if !q.From.IsZero() && e.TS.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && e.TS.After(q.To) {
		return false
	}
	if q.Search != "" {
		s := strings.ToLower(q.Search)
		if !strings.Contains(strings.ToLower(e.Target), s) && !strings.Contains(strings.ToLower(e.Message), s) {
			return false
		}
	}
	return true
}

func readLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out [][]byte
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		out = append(out, cp)
	}
	return out, sc.Err()
}

// WriteCSV 把查询结果写成 CSV（带 UTF-8 BOM，便于 Excel 正确显示中文）。
func WriteCSV(w io.Writer, entries []Entry) error {
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	header := []string{"时间", "用户", "动作", "目标", "状态", "字节", "IP", "客户端", "说明"}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, e := range entries {
		row := []string{
			e.TS.Local().Format("2006-01-02 15:04:05"),
			e.User, e.Action, e.Target, e.Status,
			strconv.FormatInt(e.Bytes, 10),
			e.IP, e.UserAgent, e.Message,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Cleanup 删除超出保留期的日志文件，返回删除的文件数。retention <= 0 表示不清理。
func (l *Logger) Cleanup(retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-retention)
	files, err := l.files()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, path := range files {
		d, ok := dateOf(path)
		if !ok {
			continue
		}
		// 文件日期的次日仍早于截止时间才删除，避免删掉当天仍在写入的文件。
		if d.Add(24 * time.Hour).Before(cutoff) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return removed, fmt.Errorf("删除日志 %s 失败: %w", filepath.Base(path), err)
			}
			removed++
		}
	}
	return removed, nil
}

// Count 返回日志总条数（用于系统页展示）。
func (l *Logger) Count() (int, error) {
	files, err := l.files()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, path := range files {
		lines, err := readLines(path)
		if err != nil {
			return total, err
		}
		total += len(lines)
	}
	return total, nil
}

// PurgeOlderThan 只删除 cutoff 之前的日志，返回删除的文件数与字节数。
//
// 日志按天存成一个文件，无法只删文件里的部分行。因此判定标准是
// "整个文件覆盖的时间段 [d, d+24h) 都早于 cutoff" 才删 —— 这样
// 只要文件里还有一条记录落在保留期内，这个文件就不会被删。
func (l *Logger) PurgeOlderThan(cutoff time.Time) (files int, bytes int64, err error) {
	all, ferr := l.files()
	if ferr != nil {
		return 0, 0, ferr
	}
	cut := cutoff.UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, path := range all {
		if !fileFullyBefore(path, cut) {
			continue
		}
		if fi, statErr := os.Stat(path); statErr == nil {
			bytes += fi.Size()
		}
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return files, bytes, fmt.Errorf("删除日志 %s 失败: %w", filepath.Base(path), rmErr)
		}
		files++
	}
	return files, bytes, nil
}

// fileFullyBefore 判断某个日志文件覆盖的时间段是否完全早于 cutoff。
func fileFullyBefore(path string, cutoff time.Time) bool {
	d, ok := dateOf(path)
	if !ok {
		return false
	}
	// 文件覆盖 [d, d+24h)。end <= cutoff 表示整段都在保留期之外。
	return !d.Add(24 * time.Hour).After(cutoff)
}

// PurgePreview 统计如果现在清理会删掉多少（不实际删除）。
func (l *Logger) PurgePreview(cutoff time.Time) (files int, bytes int64, entries int, err error) {
	all, ferr := l.files()
	if ferr != nil {
		return 0, 0, 0, ferr
	}
	cut := cutoff.UTC()
	for _, path := range all {
		if !fileFullyBefore(path, cut) {
			continue
		}
		if fi, statErr := os.Stat(path); statErr == nil {
			bytes += fi.Size()
		}
		if lines, rerr := readLines(path); rerr == nil {
			entries += len(lines)
		}
		files++
	}
	return files, bytes, entries, nil
}
