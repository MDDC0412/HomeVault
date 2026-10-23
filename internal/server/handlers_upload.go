package server

import (
	"net/http"
	"strconv"
	"time"

	"homevault/internal/state"
)

// uploadSessionView 是分片上传会话的对外结构。
type uploadSessionView struct {
	ID            string    `json:"id"`
	Dir           string    `json:"dir"`
	Name          string    `json:"name"`
	Size          int64     `json:"size"`
	ChunkSize     int64     `json:"chunkSize"`
	ChunkCount    int       `json:"chunkCount"`
	Received      []int     `json:"received"`
	BytesReceived int64     `json:"bytesReceived"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Owner         string    `json:"owner,omitempty"`
}

func sessionView(sess *state.UploadSession, withOwner bool) uploadSessionView {
	v := uploadSessionView{
		ID: sess.ID, Dir: sess.Dir, Name: sess.Name, Size: sess.Size,
		ChunkSize: sess.ChunkSize, ChunkCount: sess.ChunkCount,
		Received: sess.Received, BytesReceived: sess.BytesReceived(),
		CreatedAt: sess.CreatedAt, ExpiresAt: sess.ExpiresAt,
	}
	if v.Received == nil {
		v.Received = []int{}
	}
	if withOwner {
		v.Owner = sess.Username
	}
	return v
}

type uploadInitReq struct {
	Dir       string `json:"dir"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	ChunkSize int64  `json:"chunkSize"`
	Password  string `json:"password"`
}

// handleUploadInit 创建或复用分片上传会话（同用户+目录+文件名+大小 → 续传）。
func (s *Server) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	var req uploadInitReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireReauth(w, r, req.Password) {
		return
	}
	target := targetFrom(r)
	sess, err := s.svc.InitUpload(s.actor(r), target, req.Dir, req.Name, req.Size, req.ChunkSize)
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionView(sess, false))
}

// handleUploadList 列出未完成的上传会话（刷新后可继续）。
func (s *Server) handleUploadList(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	target := targetFrom(r)
	// 管理员未指定 user= 时可以看到全部会话，否则只看目标用户。
	showAll := actor.IsAdmin && r.URL.Query().Get("user") == ""
	all := s.svc.State.UploadSessions()
	items := make([]uploadSessionView, 0, len(all))
	for _, sess := range all {
		if !showAll && sess.Username != target.Username {
			continue
		}
		items = append(items, sessionView(sess, actor.IsAdmin))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	target := targetFrom(r)
	sess, err := s.svc.UploadSession(target, r.PathValue("id"))
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionView(sess, false))
}

// handleUploadChunk 写入一个分片（同一 index 重复写入是幂等的）。
func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if !s.requireReauth(w, r, "") {
		return
	}
	target := targetFrom(r)
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "分片序号不是整数")
		return
	}
	if r.ContentLength < 0 {
		writeErr(w, http.StatusLengthRequired, "length_required", "分片上传必须带 Content-Length")
		return
	}
	var body = r.Body
	if target.RateLimitMbps > 0 {
		body = &throttledReadCloser{rc: r.Body, rl: s.rl, ctx: r.Context(),
			key: "up:" + target.Username, mbps: target.RateLimitMbps}
	}
	sess, err := s.svc.WriteChunk(target, r.PathValue("id"), index, body, r.ContentLength,
		r.Header.Get("X-Chunk-SHA256"))
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionView(sess, false))
}

type completeReq struct {
	Password string `json:"password"`
}

// handleUploadComplete 合并分片并落盘。
func (s *Server) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	bodyPw := ""
	if r.ContentLength > 0 {
		var req completeReq
		if !decodeJSON(w, r, &req) {
			return
		}
		bodyPw = req.Password
	}
	if !s.requireReauth(w, r, bodyPw) {
		return
	}
	target := targetFrom(r)
	name, relPath, size, err := s.svc.CompleteUpload(s.actor(r), target, r.PathValue("id"))
	if err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "path": relPath, "size": size})
}

// handleUploadCancel 放弃上传并清理临时分片。
func (s *Server) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	bodyPw := ""
	if r.ContentLength > 0 {
		var req completeReq
		if !decodeJSON(w, r, &req) {
			return
		}
		bodyPw = req.Password
	}
	if !s.requireReauth(w, r, bodyPw) {
		return
	}
	target := targetFrom(r)
	if err := s.svc.CancelUpload(s.actor(r), target, r.PathValue("id")); err != nil {
		s.writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
