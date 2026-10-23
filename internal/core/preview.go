package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homevault/internal/audit"
	"homevault/internal/fsops"
	"homevault/internal/state"
)

// ErrPreviewUnavailable 表示当前环境无法生成该预览（例如没有 LibreOffice）。
// HTTP 层据此返回 409 并带上 builtin 标记，让前端改用内置渲染器。
var ErrPreviewUnavailable = errors.New("当前环境不支持该格式的服务端预览")

// 预览分类中"目录"占位（前端用 folder 区分文件夹图标）。
const KindFolder = "folder"

// PreviewPlan 描述一次预览应该怎么处理。
type PreviewPlan struct {
	Kind     string `json:"kind"` // image/video/audio/pdf/text/office/other
	Mode     string `json:"mode"` // inline（浏览器直接渲染）/ convert（服务端转 PDF）/ download
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Ext      string `json:"ext"`
	// Builtin 为真表示需要前端用内置 JS 渲染器处理。
	Builtin bool `json:"builtin"`
}

var mimeByExt = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif",
	"webp": "image/webp", "bmp": "image/bmp", "svg": "image/svg+xml", "avif": "image/avif",
	"ico": "image/x-icon", "heic": "image/heic",
	"mp4": "video/mp4", "webm": "video/webm", "mkv": "video/x-matroska", "mov": "video/quicktime",
	"ogv": "video/ogg", "m4v": "video/mp4",
	"mp3": "audio/mpeg", "wav": "audio/wav", "flac": "audio/flac", "aac": "audio/aac",
	"ogg": "audio/ogg", "m4a": "audio/mp4", "opus": "audio/opus",
	"pdf": "application/pdf",
	"txt": "text/plain; charset=utf-8", "md": "text/plain; charset=utf-8",
	"csv": "text/plain; charset=utf-8", "json": "application/json; charset=utf-8",
	"xml": "application/xml; charset=utf-8", "log": "text/plain; charset=utf-8",
	"html": "text/plain; charset=utf-8", "css": "text/plain; charset=utf-8",
	"js": "text/plain; charset=utf-8", "ts": "text/plain; charset=utf-8",
	"go": "text/plain; charset=utf-8", "py": "text/plain; charset=utf-8",
	"sh": "text/plain; charset=utf-8", "ps1": "text/plain; charset=utf-8",
	"yml": "text/plain; charset=utf-8", "yaml": "text/plain; charset=utf-8",
	"toml": "text/plain; charset=utf-8", "ini": "text/plain; charset=utf-8",
	"doc":  "application/msword",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xls":  "application/vnd.ms-excel",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"ppt":  "application/vnd.ms-powerpoint",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"odt":  "application/vnd.oasis.opendocument.text",
	"ods":  "application/vnd.oasis.opendocument.spreadsheet",
	"odp":  "application/vnd.oasis.opendocument.presentation",
	"rtf":  "application/rtf",
	"zip":  "application/zip", "7z": "application/x-7z-compressed", "rar": "application/vnd.rar",
	"tar": "application/x-tar", "gz": "application/gzip",
}

// MimeOf 按扩展名给出 MIME 类型。
func MimeOf(name string) string {
	if m, ok := mimeByExt[ExtOf(name)]; ok {
		return m
	}
	return "application/octet-stream"
}

// PlanPreview 决定某个文件如何预览。
func (s *Service) PlanPreview(u *state.User, rel string) (*PreviewPlan, error) {
	abs, err := s.Resolve(u, rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, ErrNotFound
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%w: 目录不支持预览", ErrBadRequest)
	}
	ext := ExtOf(filepath.Base(abs))
	kind := KindOf(filepath.Base(abs))
	plan := &PreviewPlan{Kind: kind, Size: fi.Size(), Ext: ext, MimeType: MimeOf(abs)}
	switch kind {
	case "image", "video", "audio", "pdf", "text":
		plan.Mode = "inline"
	case "office":
		plan.Mode = "convert"
		// 服务端没有可用的 LibreOffice（或设置要求 builtin）时，交给前端内置渲染器。
		plan.Builtin = !s.OfficeAvailable()
	default:
		plan.Mode = "download"
	}
	return plan, nil
}

// ---------------------------------------------------------------------------
// LibreOffice 探测与转换
// ---------------------------------------------------------------------------

var (
	sofficeMu      sync.Mutex
	sofficePath    string
	sofficeCheck   time.Time
	sofficeChecked bool
)

// sofficeBinary 查找 LibreOffice 可执行文件，结果缓存 60 秒。
func (s *Service) sofficeBinary() string {
	sofficeMu.Lock()
	defer sofficeMu.Unlock()
	if sofficeChecked && time.Since(sofficeCheck) < time.Minute {
		return sofficePath
	}
	sofficeChecked = true
	sofficeCheck = time.Now()
	sofficePath = ""
	// off = 完全关闭预览转换；builtin = 一律用前端内置渲染，也不探测 soffice。
	if mode := s.Settings().OfficePreview; mode == "off" || mode == "builtin" {
		return ""
	}
	if env := os.Getenv("HOMEVAULT_SOFFICE"); env != "" {
		if _, err := os.Stat(env); err == nil {
			sofficePath = env
			return sofficePath
		}
	}
	candidates := []string{"soffice", "libreoffice", "soffice.exe", "libreoffice.exe"}
	// Windows 常见安装位置
	if os.PathSeparator == '\\' {
		candidates = append(candidates,
			`C:\Program Files\LibreOffice\program\soffice.exe`,
			`C:\Program Files (x86)\LibreOffice\program\soffice.exe`)
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			sofficePath = p
			break
		}
	}
	return sofficePath
}

// OfficeAvailable 报告是否可用服务端 Office 转换。
func (s *Service) OfficeAvailable() bool { return s.sofficeBinary() != "" }

// 限制同时进行的转换数量，避免多份 LibreOffice 抢内存。
var convertSem = make(chan struct{}, 2)

// inflight 用于合并同一文件的并发转换请求。
var (
	inflightMu sync.Mutex
	inflight   = map[string]*sync.WaitGroup{}
)

// EnsureOfficePDF 把 Office 文档转换为 PDF 并返回缓存文件路径。
func (s *Service) EnsureOfficePDF(u *state.User, rel string) (string, error) {
	bin := s.sofficeBinary()
	if bin == "" {
		return "", ErrPreviewUnavailable
	}
	abs, err := s.Resolve(u, rel)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", ErrNotFound
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d", u.Root, rel, fi.Size(), fi.ModTime().UnixNano())))
	key := hex.EncodeToString(sum[:16])
	cacheDir := filepath.Join(s.Cfg.CachePath(), "office")
	cachePath := filepath.Join(cacheDir, key+".pdf")
	if st, err := os.Stat(cachePath); err == nil && st.Size() > 0 {
		return cachePath, nil
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return "", err
	}

	// 同一文件并发只转换一次
	inflightMu.Lock()
	wg, ok := inflight[key]
	if !ok {
		wg = &sync.WaitGroup{}
		wg.Add(1)
		inflight[key] = wg
		inflightMu.Unlock()

		err := s.convertToPDF(bin, abs, cachePath, cacheDir)
		wg.Done()
		inflightMu.Lock()
		delete(inflight, key)
		inflightMu.Unlock()
		if err != nil {
			return "", err
		}
		return cachePath, nil
	}
	inflightMu.Unlock()
	wg.Wait()
	if st, err := os.Stat(cachePath); err == nil && st.Size() > 0 {
		return cachePath, nil
	}
	return "", ErrPreviewUnavailable
}

func (s *Service) convertToPDF(bin, src, cachePath, cacheDir string) error {
	convertSem <- struct{}{}
	defer func() { <-convertSem }()

	tmpDir, err := os.MkdirTemp(cacheDir, "conv-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	timeout := 120 * time.Second
	cmd := exec.Command(bin,
		"--headless", "--norestore", "--nolockcheck", "--nodefault",
		"--convert-to", "pdf", "--outdir", tmpDir, src)
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 LibreOffice 失败：%w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("LibreOffice 转换失败：%w", err)
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return errors.New("LibreOffice 转换超时")
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
			tmp := filepath.Join(tmpDir, e.Name())
			if err := fsops.Move(tmp, cachePath); err != nil {
				return err
			}
			return nil
		}
	}
	return errors.New("LibreOffice 没有产出 PDF（可能是不支持的格式）")
}

// CleanupCache 清理过期的预览缓存，返回删除的文件数与字节数。
func (s *Service) CleanupCache(maxAge time.Duration) (int, int64, error) {
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	root := s.Cfg.CachePath()
	var count int
	var bytes int64
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if time.Since(info.ModTime()) < maxAge {
			return nil
		}
		if err := os.Remove(p); err == nil {
			count++
			bytes += info.Size()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return count, bytes, err
	}
	return count, bytes, nil
}

// TextPreviewLimit 是文本预览的大小上限。
const TextPreviewLimit = 1 << 20

// TextEditReadLimit 是可编辑读取的上限：超过这个体积就不建议在线编辑了。
const TextEditReadLimit = 4 << 20

// TextEditWriteLimit 是在线编辑单次保存的内容上限。
const TextEditWriteLimit = 8 << 20

// ErrNotText 表示目标不是可在线编辑的文本文件。
var ErrNotText = errors.New("该文件类型不支持在线编辑")

// ErrTextTooLarge 表示文本超出在线编辑的体积上限。
var ErrTextTooLarge = errors.New("文本超出在线编辑的体积上限")

// ReadTextPreview 读取文本文件的前若干字节用于预览。
func (s *Service) ReadTextPreview(u *state.User, rel string) (string, error) {
	abs, err := s.Resolve(u, rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", ErrNotFound
	}
	defer f.Close()
	buf := make([]byte, TextPreviewLimit)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	return string(buf[:n]), nil
}

// TextFile 是一次"打开文本文件准备编辑"的结果。
type TextFile struct {
	Content string `json:"content"`
	Size    int64  `json:"size"`
	// ModTime 用字符串传纳秒时间戳：19 位整数超出 JS 的 2^53 安全范围，
	// 用数字会在浏览器里被浮点截断，导致并发校验必然失败。
	ModTime   string `json:"modTime"`
	Truncated bool   `json:"truncated"`
}

// resolveTextFile 校验目标确实是一个允许在线编辑的文本文件。
func (s *Service) resolveTextFile(u *state.User, rel string) (abs, clean string, fi os.FileInfo, err error) {
	clean, err = fsops.CleanRel(rel)
	if err != nil {
		return "", "", nil, err
	}
	if clean == "" {
		return "", "", nil, fmt.Errorf("%w: 不能编辑根目录", ErrBadRequest)
	}
	abs, err = s.Resolve(u, clean)
	if err != nil {
		return "", "", nil, err
	}
	fi, err = os.Stat(abs)
	if err != nil {
		return "", "", nil, ErrNotFound
	}
	if fi.IsDir() {
		return "", "", nil, fmt.Errorf("%w: 目录不能按文本编辑", ErrBadRequest)
	}
	// 只允许文本类文件，避免把二进制内容当字符串改坏。
	if KindOf(filepath.Base(abs)) != "text" {
		return "", "", nil, ErrNotText
	}
	return abs, clean, fi, nil
}

// ReadTextFile 读取文本全文用于编辑（带体积上限）。
func (s *Service) ReadTextFile(u *state.User, rel string) (*TextFile, error) {
	abs, _, fi, err := s.resolveTextFile(u, rel)
	if err != nil {
		return nil, err
	}
	if fi.Size() > TextEditReadLimit {
		return nil, fmt.Errorf("%w：文件 %s，超过在线编辑上限 %s",
			ErrTextTooLarge, humanSize(fi.Size()), humanSize(TextEditReadLimit))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return &TextFile{
		Content:   string(data),
		Size:      fi.Size(),
		ModTime:   strconv.FormatInt(fi.ModTime().UnixNano(), 10),
		Truncated: false,
	}, nil
}

// WriteTextFile 把新内容原子写回文本文件。
//
// baseModTime 为打开时拿到的纳秒时间戳字符串。非空时做乐观并发校验：
// 文件在此期间被改动过就拒绝保存，避免两个人同时编辑时悄悄覆盖对方的修改。
func (s *Service) WriteTextFile(act Actor, u *state.User, rel, content string, baseModTime string) (int64, string, error) {
	abs, clean, fi, err := s.resolveTextFile(u, rel)
	if err != nil {
		return 0, "", err
	}
	newSize := int64(len(content))
	if newSize > TextEditWriteLimit {
		return 0, "", fmt.Errorf("%w：%s 超过上限 %s",
			ErrTextTooLarge, humanSize(newSize), humanSize(TextEditWriteLimit))
	}
	if baseModTime != "" {
		want, convErr := strconv.ParseInt(baseModTime, 10, 64)
		if convErr != nil {
			return 0, "", fmt.Errorf("%w: baseModTime 不是合法时间戳", ErrBadRequest)
		}
		if fi.ModTime().UnixNano() != want {
			return 0, "", fmt.Errorf("%w: 文件已被其他改动修改，请重新打开后再保存", ErrConflict)
		}
	}
	// 只有变大才需要校验配额，缩小或等长不占新空间。
	if newSize > fi.Size() {
		if err := s.CheckQuota(u, newSize-fi.Size()); err != nil {
			return 0, "", err
		}
	}

	// 同目录临时文件 + rename，保证写到一半中断不会留下半截文件。
	tmp, err := fsops.TempFile(filepath.Dir(abs))
	if err != nil {
		return 0, "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return 0, "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return 0, "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return 0, "", err
	}
	// 沿用原文件的权限位，避免保存后权限被改成临时文件的默认值。
	if err := os.Chmod(tmpName, fi.Mode().Perm()); err != nil {
		os.Remove(tmpName)
		return 0, "", err
	}
	if err := os.Rename(tmpName, abs); err != nil {
		os.Remove(tmpName)
		return 0, "", err
	}

	s.audit(act, audit.ActionTextSave, clean, audit.StatusOK, newSize,
		fmt.Sprintf("%s → %s", humanSize(fi.Size()), humanSize(newSize)))
	s.InvalidateUsage(u.Root)

	// 回传保存后的 mtime，前端据此更新基线，可以继续连续保存。
	var newMod string
	if st, statErr := os.Stat(abs); statErr == nil {
		newMod = strconv.FormatInt(st.ModTime().UnixNano(), 10)
	}
	return newSize, newMod, nil
}
