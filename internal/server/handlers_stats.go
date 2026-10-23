package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"homevault/internal/audit"
	"homevault/internal/core"
)

// statsUserUsage 是"每个账号的用量"一行。
type statsUserUsage struct {
	Username  string `json:"username"`
	Display   string `json:"displayName,omitempty"`
	IsAdmin   bool   `json:"isAdmin"`
	Disabled  bool   `json:"disabled"`
	Quota     int64  `json:"quota"`
	Used      int64  `json:"used"`
	TrashUsed int64  `json:"trashUsed"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
}

type statsKind struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

type statsActivity struct {
	Date     string `json:"date"`
	Total    int    `json:"total"`
	Upload   int    `json:"upload"`
	Download int    `json:"download"`
	Delete   int    `json:"delete"`
	Denied   int    `json:"denied"`
}

// handleStats 汇总管理端概览需要的所有数字（一次请求，避免前端拼多个接口）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st := s.svc.Settings()
	users := s.svc.State.Users()

	out := map[string]any{}
	admins, disabled := 0, 0
	usage := make([]statsUserUsage, 0, len(users))
	kindCount := map[string]*statsKind{}
	var totalUsed, totalTrash, totalFiles int64
	var fileCount int

	for _, u := range users {
		if u == nil {
			continue
		}
		if u.IsAdmin {
			admins++
		}
		if u.Disabled {
			disabled++
		}
		row := statsUserUsage{
			Username: u.Username, Display: u.DisplayName, IsAdmin: u.IsAdmin,
			Disabled: u.Disabled, Quota: u.Quota,
		}
		if strings.TrimSpace(u.Root) != "" {
			root, err := s.svc.FS.RootPath(u.Root)
			if err == nil {
				// 遍历一次：既统计用量，也统计文件类型分布
				_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					info, ierr := d.Info()
					if ierr != nil {
						return nil
					}
					size := info.Size()
					row.Bytes += size
					row.Files++
					fileCount++
					totalFiles += size
					name := d.Name()
					kind := core.KindOf(name)
					k, ok := kindCount[kind]
					if !ok {
						k = &statsKind{Kind: kind}
						kindCount[kind] = k
					}
					k.Count++
					k.Bytes += size
					return nil
				})
			}
			row.Used = row.Bytes + s.svc.PendingUploadBytes(u.Username)
			row.TrashUsed = s.svc.State.TrashUsedByRoot(u.Root)
			totalUsed += row.Used
			totalTrash += row.TrashUsed
		}
		usage = append(usage, row)
	}

	// 用量降序，管理员排最后（管理员没有空间）
	sort.SliceStable(usage, func(i, j int) bool {
		if usage[i].IsAdmin != usage[j].IsAdmin {
			return !usage[i].IsAdmin
		}
		return usage[i].Used > usage[j].Used
	})

	kinds := make([]statsKind, 0, len(kindCount))
	for _, k := range kindCount {
		kinds = append(kinds, *k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].Bytes > kinds[j].Bytes })

	// 近 7 天活动
	activity := make([]statsActivity, 0, 7)
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -6)
	res, err := s.svc.Logs.Query(audit.Query{From: from, Limit: 500})
	byDay := map[string]*statsActivity{}
	for i := 0; i < 7; i++ {
		d := from.AddDate(0, 0, i).Format("2006-01-02")
		byDay[d] = &statsActivity{Date: d}
	}
	if err == nil {
		for _, e := range res.Entries {
			d := e.TS.Local().Format("2006-01-02")
			a, ok := byDay[d]
			if !ok {
				continue
			}
			a.Total++
			switch e.Action {
			case audit.ActionUpload:
				a.Upload++
			case audit.ActionDownload:
				a.Download++
			case audit.ActionDelete:
				a.Delete++
			}
			if e.Status == audit.StatusDenied {
				a.Denied++
			}
		}
	}
	for i := 0; i < 7; i++ {
		activity = append(activity, *byDay[from.AddDate(0, 0, i).Format("2006-01-02")])
	}

	spaceUsed := totalUsed + totalTrash
	quota := int64(st.SpaceQuota)
	free := int64(0)
	if quota > 0 && quota > spaceUsed {
		free = quota - spaceUsed
	}

	out["users"] = map[string]any{
		"total": len(usage), "admins": admins, "disabled": disabled,
		"normal": len(usage) - admins,
	}
	out["space"] = map[string]any{
		"used": spaceUsed, "quota": quota, "free": free,
		"files": totalFiles, "trash": totalTrash, "fileCount": fileCount,
		"spaceDir": s.cfg.SpaceDir, "restartPending": strings.TrimSpace(st.SpaceDir) != "",
	}
	out["userUsage"] = usage
	out["kinds"] = kinds
	out["activity"] = activity
	out["server"] = map[string]any{
		"version": Version, "uptimeSeconds": int(time.Since(s.svc.StartedAt()).Seconds()),
		"officeAvailable": s.svc.OfficeAvailable(),
	}
	writeJSON(w, http.StatusOK, out)
}
