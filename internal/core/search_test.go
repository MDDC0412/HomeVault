package core

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"homevault/internal/audit"
)

var (
	dayA = time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)
	dayB = time.Date(2024, 3, 6, 12, 0, 0, 0, time.UTC)
)

// seedSearchTree 构造一棵固定的搜索树，返回根目录绝对路径。
func seedSearchTree(t *testing.T, f *fix) {
	t.Helper()
	f.ensureRoot(f.alice)
	type item struct {
		rel  string
		size int64
		at   time.Time
	}
	items := []item{
		{"docs/report.pdf", 1000, dayA},
		{"docs/notes.txt", 2000, dayA},
		{"docs/img/photo.JPG", 1500, dayA},
		{"docs/img/photo2.png", 500, dayA},
		{"videos/clip.mp4", 20 << 20, dayB},
		{"music/song.mp3", 1<<20 + 200_000, dayB},
		{"archive.zip", 3 << 20, dayA},
		{"misc/data.bin", 5, dayA},
		{"sparse/huge.iso", 3 << 29, dayA}, // 1.5 GiB，稀疏文件
	}
	for _, it := range items {
		abs := f.writeSize(f.alice, it.rel, it.size)
		if err := os.Chtimes(abs, it.at, it.at); err != nil {
			t.Fatalf("Chtimes(%s): %v", it.rel, err)
		}
	}
}

func hitNames(res *SearchResult) map[string]bool {
	m := map[string]bool{}
	for _, h := range res.Hits {
		m[h.Path] = true
	}
	return m
}

func bucketCount(res []AggBucket) int {
	n := 0
	for _, b := range res {
		n += b.Count
	}
	return n
}

func TestSearchAllFiles(t *testing.T) {
	f := newFix(t)
	seedSearchTree(t, f)
	// bob 的文件绝不能出现在 alice 的结果里
	f.ensureRoot(f.bob)
	f.write(f.bob, "secret.pdf", "nope")

	res, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 9 {
		t.Fatalf("应命中 9 个文件，实际 %d（%v）", res.Total, hitNames(res))
	}
	if len(res.Hits) != 9 {
		t.Fatalf("默认 limit=100 应返回全部 9 条，实际 %d", len(res.Hits))
	}
	if res.Truncated {
		t.Fatal("不应标记截断")
	}
	if res.Scanned < 9 {
		t.Fatalf("扫描计数异常：%d", res.Scanned)
	}
	for _, h := range res.Hits {
		if h.IsDir {
			t.Fatalf("默认不应返回目录：%+v", h)
		}
		if h.Path == "secret.pdf" {
			t.Fatal("泄漏了其它用户根目录的文件")
		}
	}
	// 类型聚合
	kinds := map[string]int{}
	for _, b := range res.ByKind {
		kinds[b.Key] = b.Count
	}
	if kinds["image"] != 2 || kinds["pdf"] != 1 || kinds["text"] != 1 ||
		kinds["video"] != 1 || kinds["audio"] != 1 || kinds["archive"] != 1 || kinds["other"] != 2 {
		t.Fatalf("按类型聚合不正确：%+v", kinds)
	}
	// 扩展名聚合（JPG 应被小写化）
	exts := map[string]int{}
	for _, b := range res.ByExt {
		exts[b.Key] = b.Count
	}
	for _, e := range []string{"pdf", "txt", "jpg", "png", "mp4", "mp3", "zip", "bin", "iso"} {
		if exts[e] != 1 {
			t.Fatalf("扩展名 %s 聚合应为 1，实际 %d（%+v）", e, exts[e], exts)
		}
	}
	// 大小分桶：始终 5 个桶且顺序固定，计数之和等于命中数
	if len(res.BySize) != 5 {
		t.Fatalf("大小桶应固定 5 个，实际 %d", len(res.BySize))
	}
	if got := bucketCount(res.BySize); got != 9 {
		t.Fatalf("大小桶计数之和应为 9，实际 %d", got)
	}
	if res.BySize[0].Key != "< 1 MiB" || res.BySize[4].Key != "> 1 GiB" {
		t.Fatalf("大小桶顺序不正确：%+v", res.BySize)
	}
	if res.BySize[4].Count != 1 {
		t.Fatalf("1.5GiB 的文件应落在最后一个桶：%+v", res.BySize[4])
	}
	// 日期聚合按天倒序
	if len(res.ByDay) != 2 {
		t.Fatalf("应有两个日期桶，实际 %+v", res.ByDay)
	}
	if res.ByDay[0].Key <= res.ByDay[1].Key {
		t.Fatalf("日期桶应倒序：%+v", res.ByDay)
	}
	// 审计
	logs, err := f.logs.Query(audit.Query{User: "alice", Action: "search"})
	if err != nil || logs.Total != 1 {
		t.Fatalf("搜索应写一条审计：%d %v", logs.Total, err)
	}
}

func TestSearchFilters(t *testing.T) {
	f := newFix(t)
	seedSearchTree(t, f)

	cases := []struct {
		name string
		opt  SearchOptions
		want []string
	}{
		{"文件名子串", SearchOptions{Query: "photo"}, []string{"docs/img/photo.JPG", "docs/img/photo2.png"}},
		{"子串不区分大小写", SearchOptions{Query: "PHOTO"}, []string{"docs/img/photo.JPG", "docs/img/photo2.png"}},
		{"扩展名过滤", SearchOptions{Exts: []string{"pdf", "TXT"}}, []string{"docs/report.pdf", "docs/notes.txt"}},
		{"扩展名带点", SearchOptions{Exts: []string{".zip"}}, []string{"archive.zip"}},
		{"类型过滤", SearchOptions{Kinds: []string{"image"}}, []string{"docs/img/photo.JPG", "docs/img/photo2.png"}},
		{"大小下界", SearchOptions{MinSize: 2 << 20}, []string{"archive.zip", "videos/clip.mp4", "sparse/huge.iso"}},
		{"大小上界", SearchOptions{MaxSize: 1000}, []string{"docs/report.pdf", "docs/img/photo2.png", "misc/data.bin"}},
		{"时间下界", SearchOptions{From: dayB}, []string{"videos/clip.mp4", "music/song.mp3"}},
		{"时间上界", SearchOptions{To: dayA}, []string{
			"docs/report.pdf", "docs/notes.txt", "docs/img/photo.JPG", "docs/img/photo2.png",
			"archive.zip", "misc/data.bin", "sparse/huge.iso"}},
		{"子树限定", SearchOptions{Path: "docs/img"}, []string{"docs/img/photo.JPG", "docs/img/photo2.png"}},
		{"组合条件", SearchOptions{Path: "docs", Query: "photo", Kinds: []string{"image"}, MinSize: 1000},
			[]string{"docs/img/photo.JPG"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := f.svc.Search(f.actAs("alice"), f.alice, c.opt)
			if err != nil {
				t.Fatal(err)
			}
			got := hitNames(res)
			if len(got) != len(c.want) {
				t.Fatalf("命中数 %d，期望 %d（%v）", len(got), len(c.want), got)
			}
			for _, w := range c.want {
				if !got[w] {
					t.Fatalf("缺少 %q（实际 %v）", w, got)
				}
			}
		})
	}
	// 子树过滤不应读到子树外的文件
	res, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Path: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 {
		t.Fatalf("docs 子树应有 4 个文件，实际 %d（%v）", res.Total, hitNames(res))
	}
	for p := range hitNames(res) {
		if len(p) < 5 || p[:5] != "docs/" {
			t.Fatalf("子树搜索返回了子树外的路径：%q", p)
		}
	}
	// 不存在的子树
	if _, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Path: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的子树应报 ErrNotFound，实际 %v", err)
	}
	if _, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Path: "docs/notes.txt"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("对文件搜索应报 ErrNotFound，实际 %v", err)
	}
}

func TestSearchDirsAndPaging(t *testing.T) {
	f := newFix(t)
	seedSearchTree(t, f)
	// 目录默认不作为结果
	res, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{IncludeDirs: true})
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	for _, h := range res.Hits {
		if h.IsDir {
			dirs[h.Path] = true
			if h.Kind != KindFolder {
				t.Fatalf("目录的 kind 应为 %q，实际 %q", KindFolder, h.Kind)
			}
		}
	}
	for _, d := range []string{"docs", "docs/img", "videos", "music", "misc", "sparse"} {
		if !dirs[d] {
			t.Fatalf("IncludeDirs 应包含目录 %q（实际 %v）", d, dirs)
		}
	}
	if res.Total != 15 {
		t.Fatalf("含目录应为 15 项，实际 %d", res.Total)
	}
	// 带 query 时目录也参与匹配
	res, err = f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Query: "img"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Hits) != 1 || !res.Hits[0].IsDir || res.Hits[0].Path != "docs/img" {
		t.Fatalf("query=img 应只命中目录 docs/img，实际 %+v", res.Hits)
	}
	// 分页：Total 始终是全量
	res, err = f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 || res.Total != 9 {
		t.Fatalf("limit=2 应返回 2 条但 Total=9，实际 %d/%d", len(res.Hits), res.Total)
	}
	res, err = f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Limit: 2, Offset: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Total != 9 {
		t.Fatalf("offset=8 limit=2 应返回 1 条，实际 %d/%d", len(res.Hits), res.Total)
	}
	res, err = f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Offset: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 || res.Total != 9 {
		t.Fatalf("offset 超过总数应返回空列表，实际 %d/%d", len(res.Hits), res.Total)
	}
	// limit 上限 500
	res, err = f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Limit: 5000})
	if err != nil || len(res.Hits) != 9 {
		t.Fatalf("limit 超上限应被夹到 500：%d %v", len(res.Hits), err)
	}
	// 负 limit 用默认值
	res, err = f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Limit: -3})
	if err != nil || len(res.Hits) != 9 {
		t.Fatalf("非法 limit 应回落默认：%d %v", len(res.Hits), err)
	}
}

func TestSearchAggregatesMatchHits(t *testing.T) {
	f := newFix(t)
	seedSearchTree(t, f)
	res, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// ByKind / ByExt 的计数之和都应等于命中总数（聚合覆盖所有命中，而不是分页后的子集）
	if got := bucketCount(res.ByKind); got != res.Total {
		t.Fatalf("ByKind 计数之和 %d != Total %d", got, res.Total)
	}
	if got := bucketCount(res.ByExt); got != res.Total {
		t.Fatalf("ByExt 计数之和 %d != Total %d", got, res.Total)
	}
	// ByExt 上限 15，按计数倒序
	if len(res.ByExt) > 15 {
		t.Fatalf("ByExt 最多 15 个桶，实际 %d", len(res.ByExt))
	}
	if len(res.ByExt) > 1 && res.ByExt[0].Count < res.ByExt[1].Count {
		t.Fatalf("ByExt 应按计数倒序：%+v", res.ByExt)
	}
	// 字节数累计
	var sum int64
	for _, h := range res.Hits {
		sum += h.Size
	}
	var agg int64
	for _, b := range res.ByKind {
		agg += b.Bytes
	}
	if agg != sum {
		t.Fatalf("聚合字节 %d != 命中字节 %d", agg, sum)
	}
	// 分页不影响聚合
	paged, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if paged.Total != res.Total || bucketCount(paged.ByKind) != res.Total {
		t.Fatalf("分页后聚合应保持全量：%d/%d vs %d", paged.Total, bucketCount(paged.ByKind), res.Total)
	}
}

func TestSearchByDayMatchesModTime(t *testing.T) {
	f := newFix(t)
	seedSearchTree(t, f)
	res, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for _, h := range res.Hits {
		want[h.ModTime.Format("2006-01-02")]++
	}
	got := map[string]int{}
	for _, b := range res.ByDay {
		got[b.Key] = b.Count
	}
	if len(got) != len(want) {
		t.Fatalf("日期桶数量 %d != 期望 %d（%+v / %+v）", len(got), len(want), got, want)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	for i, k := range keys {
		if res.ByDay[i].Key != k || got[k] != want[k] {
			t.Fatalf("日期桶不正确：%+v vs %+v", res.ByDay, want)
		}
	}
}

func TestKindOfAndExtOf(t *testing.T) {
	cases := map[string]string{
		"a.JPG": "image", "b.png": "image", "c.mp4": "video", "d.mp3": "audio",
		"e.pdf": "pdf", "f.txt": "text", "g.docx": "office", "h.zip": "archive",
		"i.unknownext": "other", "noext": "other", ".env": "other", "j.HEIC": "image",
	}
	for name, want := range cases {
		if got := KindOf(name); got != want {
			t.Fatalf("KindOf(%q)=%q，期望 %q", name, got, want)
		}
	}
	if got := ExtOf("a.B.PDF"); got != "pdf" {
		t.Fatalf("ExtOf 应返回小写扩展名，实际 %q", got)
	}
	if got := ExtOf("noext"); got != "" {
		t.Fatalf("无扩展名应返回空串，实际 %q", got)
	}
}

func TestSearchOnMissingStorage(t *testing.T) {
	f := newFix(t)
	if _, err := f.svc.Search(f.act(), f.admin, SearchOptions{}); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("管理员没有空间，应报 ErrNoStorage，实际 %v", err)
	}
}

func TestSearchScannedCountsTree(t *testing.T) {
	f := newFix(t)
	f.ensureRoot(f.alice)
	f.write(f.alice, "a/b/c.txt", "x")
	res, err := f.svc.Search(f.actAs("alice"), f.alice, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// 根 + a + b + c.txt = 4 个条目被扫描
	if res.Scanned != 4 {
		t.Fatalf("Scanned 应为 4，实际 %d", res.Scanned)
	}
	if abs := filepath.Join(f.cfg.FilesPath(), "alice"); abs == "" {
		t.Fatal("unreachable")
	}
}
