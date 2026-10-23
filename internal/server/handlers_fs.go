package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homevault/internal/audit"
	"homevault/internal/core"
	"homevault/internal/fsops"
	"homevault/internal/state"
)

// fsEntry 是目录列表里的一个条目。
type fsEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Ext     string    `json:"ext"`
	Kind    string    `json:"kind"`
	Mime    string    `json:"mime"`
}

func newFSEntry(relDir string, e fsops.Entry) fsEntry {
	rel := e.Name
	if relDir != "" {
		rel = relDir + "/" + e.Name
	}
	kind := core.KindOf(e.Name)
	mimeType := core.MimeOf(e.Name)
	if e.IsDir {
		kind = "folder"
		mimeType = ""
	}
	return fsEntry{
		Name: e.Name, Path: rel, IsDir: e.IsDir, Size: e.Size,
		ModTime: e.ModTime, Ext: core.ExtOf(e.Name), Kind: kind, Mime: mimeType,
	}
}

// handleList 列出目录。
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	target := targetFrom(r)
	rel := r.URL.Query().Get("path")
	_, entries, err := s.svc.List(target, rel)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	clean, _ := fsops.CleanRel(rel)
	items := make([]fsEntry, 0, len(entries))
	for _, e := range entries {
		items = append(items, newFSEntry(clean, e))
	}
	used, trash, _ := s.svc.Usage(target)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    clean,
		"parent":  parentOf(clean),
		"entries": items,
		"usage":   map[string]any{"used": used, "trashUsed": trash, "quota": target.Quota},
	})
}

func parentOf(p string) string {
	if p == "" {
		return ""
	}
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return ""
	}
	return p[:idx]
}

// handleDownload 下载/内联预览文件，支持 Range 与限速。
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	target := targetFrom(r)
	rel := r.URL.Query().Get("path")
	clean, err := fsops.CleanRel(rel)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	if clean == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "不能下载根目录")
		return
	}
	abs, err := s.svc.Resolve(target, clean)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "文件不存在")
		return
	}
	if fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "bad_request", "目录不能直接下载")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	defer f.Close()

	inline := r.URL.Query().Get("inline") == "1"
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	if cd := mime.FormatMediaType(disposition, map[string]string{"filename": filepath.Base(abs)}); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}
	w.Header().Set("Content-Type", core.MimeOf(abs))
	w.Header().Set("Accept-Ranges", "bytes")
	// 任何真实读取都留痕：预览（inline=1）与普通 GET 同样会写下载审计，
	// 否则"谁看过这个文件"在日志里查不到。
	detail := "attachment"
	if inline {
		detail = "inline"
	}
	if r.URL.Query().Get("download") == "1" {
		detail += " +download=1"
	}
	s.svc.Audit(s.actor(r), audit.ActionDownload, clean, audit.StatusOK, fi.Size(), detail)
	body := io.ReadSeeker(f)
	if target.RateLimitMbps > 0 {
		body = &throttledReadSeeker{rs: f, rl: s.rl, ctx: r.Context(),
			key: "down:" + target.Username, mbps: target.RateLimitMbps}
	}
	http.ServeContent(w, r, filepath.Base(abs), fi.ModTime(), body)
}

type throttledReadSeeker struct {
	rs   io.ReadSeeker
	rl   *RateLimiter
	ctx  context.Context
	key  string
	mbps int
}

func (t *throttledReadSeeker) Read(p []byte) (int, error) {
	if len(p) > 0 {
		if err := t.rl.Wait(t.ctx, t.key, t.mbps, len(p)); err != nil {
			return 0, err
		}
	}
	return t.rs.Read(p)
}

func (t *throttledReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return t.rs.Seek(offset, whence)
}

// ---------------------------------------------------------------------------
// 写操作
// ---------------------------------------------------------------------------

type mkdirReq struct {
	Dir      string `json:"dir"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	target := targetFrom(r)
	out, err := s.svc.Mkdir(s.actor(r), target, req.Dir, req.Name)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": out})
}

type renameReq struct {
	Path     string `json:"path"`
	NewName  string `json:"newName"`
	Password string `json:"password"`
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req renameReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	target := targetFrom(r)
	out, err := s.svc.Rename(s.actor(r), target, req.Path, req.NewName)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": out})
}

type transferReq struct {
	Paths    []string `json:"paths"`
	Dest     string   `json:"dest"`
	Password string   `json:"password"`
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	s.handleTransfer(w, r, false)
}

func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	s.handleTransfer(w, r, true)
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request, copying bool) {
	var req transferReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	target := targetFrom(r)
	act := s.actor(r)
	var out []string
	var err error
	if copying {
		out, err = s.svc.Copy(act, target, req.Paths, req.Dest)
	} else {
		out, err = s.svc.Move(act, target, req.Paths, req.Dest)
	}
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": out})
}

type deleteReq struct {
	Paths    []string `json:"paths"`
	Password string   `json:"password"`
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req deleteReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	target := targetFrom(r)
	entries, err := s.svc.Trash(s.actor(r), target, req.Paths)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"trashIds": ids, "count": len(ids)})
}

// handleSimpleUpload 处理小文件的 multipart 上传（大文件请走分片接口）。
func (s *Server) handleSimpleUpload(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	if !actor.IsAdmin {
		// 普通用户上传自己的文件，无需二次密码
	} else if !s.requireReauth(w, r, "") {
		return
	}
	target := targetFrom(r)
	relDir := r.URL.Query().Get("path")

	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "需要 multipart/form-data 请求体")
		return
	}
	settings := s.svc.Settings()
	dirAbs, err := s.svc.Resolve(target, relDir)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	if fi, err := os.Stat(dirAbs); err != nil || !fi.IsDir() {
		writeErr(w, http.StatusNotFound, "not_found", "目标目录不存在")
		return
	}
	type uploaded struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var done []uploaded
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "读取上传数据失败："+err.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		name, err := fsops.CleanName(part.FileName())
		if err != nil {
			part.Close()
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		tmp, err := fsops.TempFile(dirAbs)
		if err != nil {
			part.Close()
			s.writeCoreErr(w, err)
			return
		}
		tmpName := tmp.Name()
		limit := int64(settings.MaxUploadSize)
		var reader io.Reader = part
		if limit > 0 {
			reader = io.LimitReader(part, limit+1)
		}
		written, err := io.Copy(tmp, reader)
		part.Close()
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
			s.writeCoreErr(w, err)
			return
		}
		if limit > 0 && written > limit {
			tmp.Close()
			os.Remove(tmpName)
			writeErr(w, http.StatusRequestEntityTooLarge, "too_large",
				"文件超过单文件上限 "+state.FormatSize(limit))
			return
		}
		if err := s.svc.CheckSpace(written); err != nil {
			s.writeCoreErr(w, err)
			return
		}
		if err := s.svc.CheckQuota(target, written); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			s.writeCoreErr(w, err)
			return
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			s.writeCoreErr(w, err)
			return
		}
		tmp.Close()
		finalName, err := fsops.UniqueName(dirAbs, name)
		if err != nil {
			os.Remove(tmpName)
			s.writeCoreErr(w, err)
			return
		}
		if err := os.Rename(tmpName, filepath.Join(dirAbs, finalName)); err != nil {
			os.Remove(tmpName)
			s.writeCoreErr(w, err)
			return
		}
		rel := finalName
		if relDir != "" {
			rel = relDir + "/" + finalName
		}
		s.svc.InvalidateUsage(target.Root)
		s.svc.Audit(s.actor(r), audit.ActionUpload, rel, audit.StatusOK, written, "")
		done = append(done, uploaded{Name: finalName, Path: rel, Size: written})
	}
	if len(done) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "没有收到任何文件")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploaded": done})
}

// ---------------------------------------------------------------------------
// 文本在线编辑
// ---------------------------------------------------------------------------

// handleTextRead 读取文本文件全文，供前端编辑器打开。
func (s *Server) handleTextRead(w http.ResponseWriter, r *http.Request) {
	target := targetFrom(r)
	file, err := s.svc.ReadTextFile(target, r.URL.Query().Get("path"))
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

type textSaveReq struct {
	Path string `json:"path"`
	// Content 是完整的新文件内容。
	Content string `json:"content"`
	// BaseModTime 是打开文件时拿到的 mtime（纳秒时间戳字符串，避免 JS 精度丢失）；
	// 传空表示不做并发校验（仅供明确要覆盖的场景使用）。
	BaseModTime string `json:"baseModTime"`
	Password    string `json:"password"`
}

// handleTextSave 把编辑后的内容写回文件。
func (s *Server) handleTextSave(w http.ResponseWriter, r *http.Request) {
	var req textSaveReq
	// 文本内容最坏情况下 JSON 转义后体积会翻倍，这里留足余量。
	if !decodeJSONLimit(w, r, &req, int64(core.TextEditWriteLimit)*2+4096) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	target := targetFrom(r)
	size, modTime, err := s.svc.WriteTextFile(s.actor(r), target, req.Path, req.Content, req.BaseModTime)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"size": size, "modTime": modTime})
}

// ---------------------------------------------------------------------------
// 搜索与预览
// ---------------------------------------------------------------------------

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	target := targetFrom(r)
	q := r.URL.Query()
	opt := core.SearchOptions{
		Query:       q.Get("q"),
		Path:        q.Get("path"),
		Exts:        splitCSV(q.Get("ext")),
		Kinds:       splitCSV(q.Get("kind")),
		MinSize:     queryInt64(r, "minSize", 0),
		MaxSize:     queryInt64(r, "maxSize", 0),
		IncludeDirs: q.Get("includeDirs") == "1",
		Limit:       queryInt(r, "limit", 100),
		Offset:      queryInt(r, "offset", 0),
	}
	if from := q.Get("from"); from != "" {
		if t, err := parseDayOrTime(from); err == nil {
			opt.From = t
		} else {
			writeErr(w, http.StatusBadRequest, "bad_request", "from 时间格式不正确")
			return
		}
	}
	if to := q.Get("to"); to != "" {
		if t, err := parseDayOrTime(to); err == nil {
			opt.To = t
		} else {
			writeErr(w, http.StatusBadRequest, "bad_request", "to 时间格式不正确")
			return
		}
	}
	res, err := s.svc.Search(s.actor(r), target, opt)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseDayOrTime(v string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q", v)
}

// handlePreviewPlan 告诉前端该怎么预览某个文件。
func (s *Server) handlePreviewPlan(w http.ResponseWriter, r *http.Request) {
	target := targetFrom(r)
	plan, err := s.svc.PlanPreview(target, r.URL.Query().Get("path"))
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleOfficePreview 把 Office 文档转成 PDF 后内联返回。
func (s *Server) handleOfficePreview(w http.ResponseWriter, r *http.Request) {
	target := targetFrom(r)
	rel := r.URL.Query().Get("path")
	if !s.svc.OfficeAvailable() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "服务端未安装 LibreOffice，无法转换该格式", "code": "office_preview_unavailable",
			"builtin": true,
		})
		return
	}
	pdfPath, err := s.svc.EnsureOfficePDF(target, rel)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	f, err := os.Open(pdfPath)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	s.svc.Audit(s.actor(r), audit.ActionPreview, rel, audit.StatusOK, fi.Size(), "office→pdf")
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeContent(w, r, "preview.pdf", fi.ModTime(), f)
}
