//go:build windows

package server

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Disk 描述一个可选的存储位置。
//
// 字段与 unix 版保持一致，前端只写一次。
type Disk struct {
	Path     string   `json:"path"`
	Device   string   `json:"device,omitempty"`
	Total    int64    `json:"total"`
	Free     int64    `json:"free"`
	FSType   string   `json:"fsType,omitempty"`
	Remarks  string   `json:"remarks,omitempty"`
	AltPaths []string `json:"altPaths,omitempty"`
	Writable bool     `json:"writable"`
}

// BlockDevice 在 Windows 上复用盘符列表，因此这里只需要类型占位。
type BlockDevice struct {
	Name   string   `json:"name"`
	Size   int64    `json:"size"`
	Model  string   `json:"model,omitempty"`
	Mounts []string `json:"mounts,omitempty"`
}

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFree   = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetLogicalDrv = kernel32.NewProc("GetLogicalDrives")
	procGetDriveType  = kernel32.NewProc("GetDriveTypeW")
)

// DiskStats 返回指定路径所在卷的总量与可用量。
func DiskStats(path string) (int64, int64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var freeToCaller, total, totalFree uint64
	r, _, e := procGetDiskFree.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0, 0, fmt.Errorf("GetDiskFreeSpaceEx 失败：%v", e)
	}
	return int64(total), int64(freeToCaller), nil
}

// ListDisks 枚举本机盘符。
func ListDisks() ([]Disk, error) {
	mask, _, _ := procGetLogicalDrv.Call()
	var out []Disk
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := fmt.Sprintf("%c:\\", 'A'+i)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		total, free, err := DiskStats(root)
		if err != nil {
			continue
		}
		remarks := ""
		// 2 = 可移动磁盘，3 = 固定磁盘，4 = 网络驱动器
		driveType := driveType(root)
		switch driveType {
		case 2:
			remarks = "可移动磁盘"
		case 3:
			remarks = "本地磁盘"
		case 4:
			remarks = "网络驱动器"
		case 5:
			remarks = "光驱"
		}
		out = append(out, Disk{
			Path: root, Device: root, Total: total, Free: free,
			Remarks: remarks, Writable: true,
		})
	}
	return out, nil
}

// ListBlockDevices 在 Windows 上不需要：盘符列表本身就是"一块盘一个条目"，
// 不存在 unix 下同一设备被重复挂载的问题，因此返回空。
func ListBlockDevices() ([]BlockDevice, error) { return nil, nil }

// writableDir 判断当前用户能否在目录里创建文件。
func writableDir(path string) bool {
	f, err := os.CreateTemp(path, ".hv-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func driveType(root string) uintptr {
	p, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return 0
	}
	t, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(p)))
	return t
}
