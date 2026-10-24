//go:build !windows

package server

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Disk 描述一个可选的存储位置。
//
// 关键语义：**同一块设备只出现一次**。`/proc/mounts` 里同一块盘会被挂载很多次
// （bind mount、同一分区的多个挂载点），按挂载点枚举会让一个盘看起来像好几个盘，
// 甚至把 `-v /etc/hosts:/etc/hosts` 这种**文件**挂载也当成可选位置。
type Disk struct {
	Path     string   `json:"path"`
	Device   string   `json:"device,omitempty"` // /dev/sda2 或 overlay
	Total    int64    `json:"total"`
	Free     int64    `json:"free"`
	FSType   string   `json:"fsType,omitempty"`
	Remarks  string   `json:"remarks,omitempty"`
	AltPaths []string `json:"altPaths,omitempty"` // 同一设备上的其他挂载点
	Writable bool     `json:"writable"`
}

// BlockDevice 是宿主机的块设备概览，用于让用户在管理页面看清"到底几块盘"。
//
// 容器里看不到宿主机的完整挂载表，但 /proc/partitions 与 /sys/block 是宿主机的，
// 因此可以列出所有物理盘；未被挂进容器的盘会显示为"未挂载到容器"。
type BlockDevice struct {
	Name   string   `json:"name"`
	Size   int64    `json:"size"`
	Model  string   `json:"model,omitempty"`
	Mounts []string `json:"mounts,omitempty"`
}

// DiskStats 返回指定路径所在文件系统的总量与可用量。
func DiskStats(path string) (int64, int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	total := int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	return total, free, nil
}

// skipFS 是与用户文件无关的伪文件系统。
var skipFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"cgroup": true, "cgroup2": true, "squashfs": true, "debugfs": true,
	"securityfs": true, "pstore": true, "bpf": true, "tracefs": true, "autofs": true,
	"mqueue": true, "hugetlbfs": true, "fusectl": true, "configfs": true, "ramfs": true,
	"binfmt_misc": true, "efivarfs": true, "nsfs": true, "rpc_pipefs": true,
	"fuse.lxcfs": true, "fuse.gvfsd-fuse": true, "fuseblk": true,
}

// networkFS 标注出来，避免用户把网络挂载当成"本机磁盘"。
var networkFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "sshfs": true}

type mountEntry struct {
	dev    string
	mount  string
	fsType string
}

func readMounts() ([]mountEntry, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []mountEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		out = append(out, mountEntry{
			dev:    strings.ReplaceAll(fields[0], `\040`, " "),
			mount:  strings.ReplaceAll(fields[1], `\040`, " "),
			fsType: fields[2],
		})
	}
	return out, sc.Err()
}

// ListDisks 返回按设备去重后的可写存储位置。
func ListDisks() ([]Disk, error) {
	entries, err := readMounts()
	if err != nil {
		return nil, err
	}

	type group struct {
		device string
		fsType string
		mounts []string
	}
	order := make([]uint64, 0, 8)
	groups := make(map[uint64]*group, 8)

	for _, e := range entries {
		if skipFS[e.fsType] || !strings.HasPrefix(e.mount, "/") {
			continue
		}
		// overlay 是容器根文件系统：列出来只会诱导用户把数据写进容器层。
		// 真正的宿主机磁盘在下面的 ListBlockDevices 里单独展示。
		if !strings.HasPrefix(e.dev, "/dev/") {
			continue
		}
		fi, statErr := os.Stat(e.mount)
		if statErr != nil || !fi.IsDir() {
			continue // 过滤掉 -v /etc/hosts:/etc/hosts 这类**文件**挂载
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		key := uint64(st.Dev)
		g, ok := groups[key]
		if !ok {
			g = &group{device: e.dev, fsType: e.fsType}
			groups[key] = g
			order = append(order, key)
		}
		if !containsString(g.mounts, e.mount) {
			g.mounts = append(g.mounts, e.mount)
		}
	}

	out := make([]Disk, 0, len(order))
	for _, key := range order {
		g := groups[key]
		primary := pickPrimaryMount(g.mounts)
		total, free, statErr := DiskStats(primary)
		if statErr != nil {
			continue
		}
		alt := make([]string, 0, len(g.mounts))
		for _, m := range g.mounts {
			if m != primary {
				alt = append(alt, m)
			}
		}
		sort.Strings(alt)

		d := Disk{
			Path:     primary,
			Device:   g.device,
			Total:    total,
			Free:     free,
			FSType:   g.fsType,
			AltPaths: alt,
			Writable: writableDir(primary),
		}
		var notes []string
		switch {
		case g.fsType == "overlay":
			notes = append(notes, "容器根目录，容器重建后会丢失")
		case networkFS[g.fsType]:
			notes = append(notes, "网络文件系统")
		}
		if !d.Writable {
			notes = append(notes, "当前运行用户不可写")
		}
		d.Remarks = strings.Join(notes, "；")
		out = append(out, d)
	}

	if len(out) == 0 {
		if total, free, statErr := DiskStats("/"); statErr == nil {
			out = append(out, Disk{
				Path: "/", Total: total, Free: free, FSType: "unknown",
				Writable: writableDir("/"), Remarks: "兜底项：未能枚举出具体设备",
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// ListBlockDevices 读取宿主机的 /proc/partitions，按"整盘"聚合。
//
// 容器内看不到宿主机挂载表，但这两处是宿主机视角，因此能回答
// "这台机器到底有几块盘、分别多大、哪块没挂进容器"。
func ListBlockDevices() ([]BlockDevice, error) {
	f, err := os.Open("/proc/partitions")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type raw struct {
		name string
		size int64
	}
	var all []raw
	names := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 4 || fields[0] == "major" {
			continue
		}
		blocks, convErr := strconv.ParseInt(fields[2], 10, 64)
		if convErr != nil || blocks <= 0 {
			continue
		}
		name := fields[3]
		switch {
		case strings.HasPrefix(name, "loop"),
			strings.HasPrefix(name, "ram"),
			strings.HasPrefix(name, "zram"),
			strings.HasPrefix(name, "sr"),
			strings.HasPrefix(name, "fd"):
			continue
		}
		all = append(all, raw{name: name, size: blocks * 1024})
		names[name] = true
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	entries, _ := readMounts()
	mountsOf := func(disk string) []string {
		var out []string
		for _, e := range entries {
			if !strings.HasPrefix(e.mount, "/") || skipFS[e.fsType] || e.fsType == "overlay" {
				continue
			}
			if partOf(filepath.Base(e.dev), disk) {
				out = append(out, e.mount)
			}
		}
		sort.Strings(out)
		return out
	}

	out := make([]BlockDevice, 0, len(all))
	for _, r := range all {
		if isPartition(r.name, names) {
			continue // 只列整盘，分区归到所属盘
		}
		out = append(out, BlockDevice{
			Name:   r.name,
			Size:   r.size,
			Model:  readModel(r.name),
			Mounts: mountsOf(r.name),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out, nil
}

// partOf 判断 base（如 sda1、nvme0n1p1）是否属于整盘 disk（sda、nvme0n1）。
func partOf(base, disk string) bool {
	if base == disk {
		return true
	}
	if !strings.HasPrefix(base, disk) {
		return false
	}
	rest := strings.TrimPrefix(base[len(disk):], "p")
	if rest == "" {
		return false
	}
	_, err := strconv.Atoi(rest)
	return err == nil
}

// isPartition 判断 name 是否是另一块盘的分区（sda1、nvme0n1p1、mmcblk0p1）。
func isPartition(name string, all map[string]bool) bool {
	for base := range all {
		if base == name {
			continue
		}
		if partOf(name, base) {
			return true
		}
	}
	return false
}

func readModel(name string) string {
	raw, err := os.ReadFile(filepath.Join("/sys/block", name, "device", "model"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// pickPrimaryMount 在一组挂载点里挑最能代表这块盘的那个：
// 优先当前用户可写、且不是 "/" 的（"/" 通常是只读的宿主机根或容器 overlay）。
func pickPrimaryMount(mounts []string) string {
	best, bestScore := mounts[0], -1
	for _, m := range mounts {
		score := 0
		if writableDir(m) {
			score += 2
		}
		if m != "/" {
			score++
		}
		// 同分时取更短、更靠前的路径，保证结果稳定。
		if score > bestScore || (score == bestScore && m < best) {
			best, bestScore = m, score
		}
	}
	return best
}

func writableDir(path string) bool {
	return syscall.Access(path, 0x2 /* W_OK */) == nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
