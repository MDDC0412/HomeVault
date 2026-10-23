package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"homevault/internal/fsops"
	"homevault/internal/state"
)

// 单次搜索的上限，防止在超大目录树上把磁盘和内存打满。
const (
	maxSearchScan = 300_000
	maxSearchHits = 5_000
)

// SearchOptions 描述一次搜索。
type SearchOptions struct {
	Query       string    // 文件名子串（不区分大小写），空表示不按名称过滤
	Path        string    // 限定子树（用户根内相对路径），空表示整个根
	Exts        []string  // 文件扩展名过滤（不含点，小写），空表示不过滤
	Kinds       []string  // 类型过滤：image/video/audio/pdf/text/office/archive/other
	MinSize     int64     // 字节，含
	MaxSize     int64     // 字节，含；0 表示不限
	From        time.Time // 修改时间下界
	To          time.Time // 修改时间上界
	IncludeDirs bool      // 是否把目录也作为结果返回
	Limit       int
	Offset      int
}

// SearchHit 是一条搜索结果。
type SearchHit struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Ext     string    `json:"ext"`
	Kind    string    `json:"kind"`
}

// AggBucket 是一个聚合桶（按类型、大小区间或日期）。
type AggBucket struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

// SearchResult 是搜索结果 + 聚合信息。
type SearchResult struct {
	Hits      []SearchHit `json:"hits"`
	Total     int         `json:"total"`
	Scanned   int         `json:"scanned"`
	Truncated bool        `json:"truncated"`
	ByKind    []AggBucket `json:"byKind"`
	ByExt     []AggBucket `json:"byExt"`
	BySize    []AggBucket `json:"bySize"`
	ByDay     []AggBucket `json:"byDay"`
}

// 文件类型分类用的扩展名表。
var kindExts = map[string][]string{
	"image":   {"jpg", "jpeg", "png", "gif", "webp", "bmp", "svg", "avif", "ico", "heic"},
	"video":   {"mp4", "mkv", "mov", "avi", "webm", "flv", "wmv", "m4v", "ts"},
	"audio":   {"mp3", "wav", "flac", "aac", "ogg", "m4a", "opus", "wma"},
	"pdf":     {"pdf"},
	"text":    {"txt", "md", "markdown", "log", "csv", "tsv", "json", "yaml", "yml", "xml", "ini", "conf", "toml", "go", "js", "ts", "tsx", "jsx", "py", "java", "c", "h", "cpp", "hpp", "cs", "rs", "rb", "php", "sh", "ps1", "bat", "sql", "html", "css", "scss"},
	"office":  {"doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp", "rtf"},
	"archive": {"zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar"},
}

var extKind = func() map[string]string {
	m := map[string]string{}
	for kind, list := range kindExts {
		for _, e := range list {
			m[e] = kind
		}
	}
	return m
}()

// KindOf 按扩展名判断文件类型，供前端选择预览/图标。
func KindOf(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if k, ok := extKind[ext]; ok {
		return k
	}
	return "other"
}

// ExtOf 返回小写扩展名（不含点）。
func ExtOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// Search 在用户根目录（或其子树）中递归搜索，并返回多因素聚合。
func (s *Service) Search(act Actor, u *state.User, opt SearchOptions) (*SearchResult, error) {
	rootAbs, err := s.RootAbs(u)
	if err != nil {
		return nil, err
	}
	base := rootAbs
	baseRel := ""
	if strings.TrimSpace(opt.Path) != "" {
		clean, err := fsops.CleanRel(opt.Path)
		if err != nil {
			return nil, err
		}
		base, err = s.FS.Resolve(u.Root, clean)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
			return nil, ErrNotFound
		}
		baseRel = clean
	}

	query := strings.ToLower(strings.TrimSpace(opt.Query))
	extFilter := map[string]bool{}
	for _, e := range opt.Exts {
		extFilter[strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	kindFilter := map[string]bool{}
	for _, k := range opt.Kinds {
		kindFilter[strings.ToLower(k)] = true
	}
	if opt.Limit <= 0 {
		opt.Limit = 100
	}
	if opt.Limit > 500 {
		opt.Limit = 500
	}

	res := &SearchResult{
		Hits: []SearchHit{}, ByKind: []AggBucket{}, ByExt: []AggBucket{},
		BySize: []AggBucket{}, ByDay: []AggBucket{},
	}
	kindAgg := map[string]*AggBucket{}
	extAgg := map[string]*AggBucket{}
	dayAgg := map[string]*AggBucket{}
	sizeAgg := map[string]*AggBucket{}
	sizeBuckets := []struct {
		key string
		max int64
	}{
		{"< 1 MiB", 1 << 20},
		{"1–10 MiB", 10 << 20},
		{"10–100 MiB", 100 << 20},
		{"100 MiB–1 GiB", 1 << 30},
		{"> 1 GiB", 0},
	}
	for _, b := range sizeBuckets {
		sizeAgg[b.key] = &AggBucket{Key: b.key}
	}

	matched := 0
	walkErr := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目不可读不应中断整个搜索
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		res.Scanned++
		if res.Scanned > maxSearchScan {
			res.Truncated = true
			return filepath.SkipAll
		}
		isDir := d.IsDir()
		if isDir && p != base && !opt.IncludeDirs && query == "" {
			// 目录不计入普通搜索，但仍需继续下钻
			return nil
		}
		if p == base {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		name := d.Name()
		ext := ExtOf(name)
		kind := KindOf(name)
		if isDir {
			kind = "folder"
		} else {
			if len(extFilter) > 0 && !extFilter[ext] {
				return nil
			}
			if len(kindFilter) > 0 && !kindFilter[kind] {
				return nil
			}
			if opt.MinSize > 0 && info.Size() < opt.MinSize {
				return nil
			}
			if opt.MaxSize > 0 && info.Size() > opt.MaxSize {
				return nil
			}
		}
		if !opt.From.IsZero() && info.ModTime().Before(opt.From) {
			return nil
		}
		if !opt.To.IsZero() && info.ModTime().After(opt.To) {
			return nil
		}
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return nil
		}
		relOut := filepath.ToSlash(rel)
		if baseRel != "" {
			relOut = baseRel + "/" + relOut
		}
		matched++
		// 聚合统计覆盖所有命中项
		bump(kindAgg, kind, info.Size())
		if !isDir {
			bump(extAgg, ext, info.Size())
			day := info.ModTime().Format("2006-01-02")
			bump(dayAgg, day, info.Size())
			for _, b := range sizeBuckets {
				if b.max == 0 || info.Size() < b.max {
					bump(sizeAgg, b.key, info.Size())
					break
				}
			}
		}
		if matched > opt.Offset && len(res.Hits) < opt.Limit {
			res.Hits = append(res.Hits, SearchHit{
				Path: relOut, Name: name, IsDir: isDir, Size: info.Size(),
				ModTime: info.ModTime(), Ext: ext, Kind: kind,
			})
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return nil, walkErr
	}
	res.Total = matched
	res.ByKind = topBuckets(kindAgg, 0)
	res.ByExt = topBuckets(extAgg, 15)
	res.BySize = orderedBuckets(sizeAgg, sizeBuckets)
	res.ByDay = topBuckets(dayAgg, 30)
	sort.Slice(res.ByDay, func(i, j int) bool { return res.ByDay[i].Key > res.ByDay[j].Key })

	s.audit(act, "search", opt.Query, "ok", 0, "命中 "+itoa(matched)+" 项")
	return res, nil
}

func bump(m map[string]*AggBucket, key string, size int64) {
	b, ok := m[key]
	if !ok {
		b = &AggBucket{Key: key}
		m[key] = b
	}
	b.Count++
	b.Bytes += size
}

func topBuckets(m map[string]*AggBucket, limit int) []AggBucket {
	out := make([]AggBucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func orderedBuckets(m map[string]*AggBucket, order []struct {
	key string
	max int64
}) []AggBucket {
	out := make([]AggBucket, 0, len(order))
	for _, o := range order {
		if b, ok := m[o.key]; ok {
			out = append(out, *b)
		} else {
			out = append(out, AggBucket{Key: o.key})
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
