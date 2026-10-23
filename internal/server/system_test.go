package server_test

import (
	"net/http"
	"runtime"
	"testing"
)

// 验证系统信息与磁盘枚举接口（磁盘代码是平台相关的，必须真跑一次）。
func TestSystemAndDisks(t *testing.T) {
	e := newEnv(t)
	e.loginAs("admin", "admin")

	sys := e.mustJSON("GET", "/api/system", nil, http.StatusOK)
	if sys["version"] == nil || sys["spaceDir"] == nil {
		t.Fatalf("系统信息缺少字段：%v", sys)
	}
	if int64(sys["diskTotal"].(float64)) <= 0 {
		t.Fatalf("磁盘总量应当大于 0：%v", sys["diskTotal"])
	}
	if sys["isAdmin"] != true {
		t.Fatalf("管理员应为 isAdmin=true：%v", sys)
	}

	disks := e.mustJSON("GET", "/api/system/disks", nil, http.StatusOK)
	list, ok := disks["disks"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("%s 上应当至少枚举出一个磁盘：%v", runtime.GOOS, disks)
	}
	first := list[0].(map[string]any)
	if first["path"] == nil || first["total"] == nil {
		t.Fatalf("磁盘字段不完整：%v", first)
	}
	if disks["currentSpaceDir"] != e.cfg.SpaceDir {
		t.Fatalf("当前空间目录不正确：%v != %v", disks["currentSpaceDir"], e.cfg.SpaceDir)
	}
	if disks["defaultSpaceDir"] == nil || disks["defaultSpaceDir"] == "" {
		t.Fatalf("缺少默认空间目录：%v", disks)
	}

	// 普通用户也能看到基础系统信息，但看不到账号统计
	e.mustJSON("POST", "/api/users", map[string]any{
		"username": "carol", "password": "carol12345", "root": "carol",
		"adminPassword": "admin",
	}, http.StatusOK)
	e.logout()
	e.loginAs("carol", "carol12345")
	userSys := e.mustJSON("GET", "/api/system", nil, http.StatusOK)
	if userSys["isAdmin"] != false {
		t.Fatalf("普通用户的 isAdmin 应为 false：%v", userSys)
	}
	if _, leaked := userSys["userCount"]; leaked {
		t.Fatalf("普通用户不应看到账号统计：%v", userSys)
	}
	// 普通用户不该能看磁盘列表（属于管理端能力）
	r := e.json("GET", "/api/system/disks", nil)
	if r.status == http.StatusOK {
		t.Fatalf("普通用户应无权查看磁盘列表：%d %s", r.status, r.body)
	}
}
