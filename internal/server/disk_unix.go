//go:build !windows

package server

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

// Disk 描述一个可选的存储位置。
type Disk struct {
	Path    string `json:"path"`
	Total   int64  `json:"total"`
	Free    int64  `json:"free"`
	FSType  string `json:"fsType,omitempty"`
	Remarks string `json:"remarks,omitempty"`
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

// ListDisks 读取 /proc/mounts，返回可写的本地文件系统。
func ListDisks() ([]Disk, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	skipFS := map[string]bool{
		"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
		"cgroup": true, "cgroup2": true, "overlay": true, "squashfs": true, "debugfs": true,
		"securityfs": true, "pstore": true, "bpf": true, "tracefs": true, "autofs": true,
		"mqueue": true, "hugetlbfs": true, "fusectl": true, "configfs": true, "ramfs": true,
		"binfmt_misc": true, "efivarfs": true, "nsfs": true, "rpc_pipefs": true,
	}
	seen := map[string]bool{}
	var out []Disk
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		dev, mount, fsType := fields[0], fields[1], fields[2]
		if skipFS[fsType] || !strings.HasPrefix(dev, "/dev/") {
			continue
		}
		if seen[mount] {
			continue
		}
		seen[mount] = true
		total, free, err := DiskStats(mount)
		if err != nil {
			continue
		}
		out = append(out, Disk{Path: mount, Total: total, Free: free, FSType: fsType})
	}
	if len(out) == 0 {
		if total, free, err := DiskStats("/"); err == nil {
			out = append(out, Disk{Path: "/", Total: total, Free: free, FSType: "unknown"})
		}
	}
	return out, nil
}
