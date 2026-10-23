// homevault：轻量自主托管云盘。单个二进制、零第三方依赖、开箱即用。
//
// 用法：
//
//	homevault serve                     启动服务（默认命令）
//	homevault migrate-space --to <目录> 迁移整个空间目录到新位置
//	homevault setpass -user admin       重置某个账号的密码
//	homevault adduser -user u -password p -root u   新增普通用户
//	homevault version                   打印版本
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"homevault/internal/audit"
	"homevault/internal/auth"
	"homevault/internal/config"
	"homevault/internal/core"
	"homevault/internal/fsops"
	"homevault/internal/server"
	"homevault/internal/state"
)

//go:embed all:web
var webFS embed.FS

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return cmdServe(args)
	case "migrate-space":
		return cmdMigrate(args)
	case "setpass":
		return cmdSetPass(args)
	case "adduser":
		return cmdAddUser(args)
	case "version", "-v", "--version":
		fmt.Println("homevault " + server.Version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(helpText)
		return nil
	default:
		return fmt.Errorf("未知命令 %q（可用：serve / migrate-space / setpass / adduser / version）", cmd)
	}
}

const helpText = `homevault —— 轻量自主托管云盘

用法：
  homevault serve                                 启动服务（默认）
  homevault serve -config /etc/homevault/config.ini
  homevault migrate-space --to /mnt/big/HomeVaultSpace
  homevault setpass -user admin [-password 新密码]
  homevault adduser -user zhangsan -password 密码 [-root zhangsan] [-quota 10GiB] [-admin]
  homevault version

说明：
  配置文件只需要 listen / space_dir / tls_cert / tls_key 四项，
  其余参数（配额、保留期、限速、分片大小等）在管理页面里改，立即生效。
  首次启动会创建管理员（默认 admin/admin），登录后必须修改账号或密码才能使用其他功能。
`

// ---------------------------------------------------------------------------
// serve
// ---------------------------------------------------------------------------

func cmdServe(args []string) error {
	fl := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fl.String("config", "config.ini", "配置文件路径")
	logLevel := fl.String("log-level", "info", "日志级别：debug / info / warn / error")
	if err := fl.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	switch strings.ToLower(*logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.EnsureLayout(); err != nil {
		return fmt.Errorf("创建空间目录失败：%w", err)
	}

	firstRun := !cfg.HasState()
	st, err := state.Open(cfg.StatePath())
	if err != nil {
		return fmt.Errorf("读取元数据失败：%w", err)
	}
	settings, err := st.EnsureSettings(state.DefaultSettings())
	if err != nil {
		return fmt.Errorf("初始化设置失败：%w", err)
	}

	if firstRun {
		if err := bootstrapAdmin(st, cfg); err != nil {
			return err
		}
	}

	ops, err := fsops.New(cfg.FilesPath())
	if err != nil {
		return fmt.Errorf("初始化存储目录失败：%w", err)
	}
	logs, err := audit.New(cfg.AuditPath())
	if err != nil {
		return fmt.Errorf("初始化审计日志失败：%w", err)
	}
	tokens, err := auth.NewTokenManager(settings.SessionDur())
	if err != nil {
		return err
	}
	svc := core.New(cfg, st, ops, logs, tokens)

	staticFS, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	srv := server.New(cfg, svc, staticFS, logger)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 大文件上传/下载可能持续很久，因此不设 ReadTimeout / WriteTimeout。
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go backgroundTasks(ctx, svc, logs, logger)

	printStartup(cfg, firstRun, svc)

	errCh := make(chan error, 1)
	go func() {
		if cfg.TLSEnabled() {
			errCh <- httpSrv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			errCh <- httpSrv.ListenAndServe()
		}
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("收到退出信号，正在停止服务…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("优雅退出超时", "err", err)
		}
		if err := st.Save(); err != nil {
			logger.Warn("保存元数据失败", "err", err)
		}
		logger.Info("已停止")
		return nil
	}
}

// bootstrapAdmin 首次启动创建管理员账号。
func bootstrapAdmin(st *state.Store, cfg *config.Config) error {
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	perms := map[string]bool{}
	for _, p := range state.AllPermissions {
		perms[p] = true
	}
	admin := &state.User{
		Username: cfg.AdminUser, DisplayName: "系统管理员",
		PasswordHash: hash, IsAdmin: true, Root: "",
		Permissions: perms, CreatedAt: time.Now().UTC(),
		MustChangeCredentials: true,
	}
	return st.PutUser(admin)
}

func printStartup(cfg *config.Config, firstRun bool, svc *core.Service) {
	scheme := "http"
	if cfg.TLSEnabled() {
		scheme = "https"
	}
	fmt.Println()
	fmt.Println("  homevault " + server.Version + " 已启动")
	fmt.Println("  ─────────────────────────────────────────────")
	fmt.Println("  用户端：  " + scheme + "://" + displayHost(cfg.Listen) + "/")
	fmt.Println("  管理端：  " + scheme + "://" + displayHost(cfg.Listen) + "/admin/")
	fmt.Println("  空间目录：" + cfg.SpaceDir)
	fmt.Println("  配置文件：" + cfg.Path)
	fmt.Println("  Office 预览：" + map[bool]string{true: "可用（已检测到 LibreOffice）", false: "不可用（未检测到 LibreOffice，将退回内置预览）"}[svc.OfficeAvailable()])
	if firstRun {
		fmt.Println("  ─────────────────────────────────────────────")
		fmt.Println("  首次启动已创建管理员：" + cfg.AdminUser)
		fmt.Println("  登录后必须修改账号或密码，否则无法使用其他功能。")
	}
	fmt.Println()
}

func displayHost(listen string) string {
	if strings.HasPrefix(listen, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(listen, "0.0.0.0:")
	}
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
}

// backgroundTasks 负责回收站过期、上传会话过期、日志与缓存清理。
func backgroundTasks(ctx context.Context, svc *core.Service, logs *audit.Logger, logger *slog.Logger) {
	trashTick := time.NewTicker(15 * time.Minute)
	hourlyTick := time.NewTicker(time.Hour)
	defer trashTick.Stop()
	defer hourlyTick.Stop()

	runTrash := func() {
		if n, bytes, err := svc.PurgeExpiredTrash(); err != nil {
			logger.Warn("清理过期回收站失败", "err", err)
		} else if n > 0 {
			logger.Info("已清理过期回收站条目", "count", n, "bytes", bytes)
		}
	}
	runHourly := func() {
		if n, err := svc.CleanupUploadSessions(); err != nil {
			logger.Warn("清理过期上传会话失败", "err", err)
		} else if n > 0 {
			logger.Info("已清理过期上传会话", "count", n)
		}
		if err := svc.CleanupExpiredUploadsDir(); err != nil {
			logger.Warn("清理上传临时目录失败", "err", err)
		}
		if n, bytes, err := svc.CleanupCache(24 * time.Hour); err != nil {
			logger.Warn("清理预览缓存失败", "err", err)
		} else if n > 0 {
			logger.Info("已清理预览缓存", "files", n, "bytes", bytes)
		}
		if n, err := logs.Cleanup(svc.Settings().LogDur()); err != nil {
			logger.Warn("清理审计日志失败", "err", err)
		} else if n > 0 {
			logger.Info("已清理过期审计日志", "files", n)
		}
	}

	runTrash()
	runHourly()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trashTick.C:
			runTrash()
		case <-hourlyTick.C:
			runHourly()
		}
	}
}

// ---------------------------------------------------------------------------
// migrate-space
// ---------------------------------------------------------------------------

func cmdMigrate(args []string) error {
	fl := flag.NewFlagSet("migrate-space", flag.ContinueOnError)
	cfgPath := fl.String("config", "config.ini", "配置文件路径")
	to := fl.String("to", "", "目标空间目录（必须为空或已包含 state.json）")
	yes := fl.Bool("yes", false, "确认执行（会复制全部数据，可能很慢）")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("必须用 --to 指定目标目录")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	from := cfg.SpaceDir
	dstAbs, err := filepath.Abs(*to)
	if err != nil {
		return err
	}
	if dstAbs == from {
		return errors.New("目标目录与当前空间目录相同")
	}
	if strings.HasPrefix(dstAbs, from+string(os.PathSeparator)) {
		return errors.New("目标目录不能位于当前空间目录内部")
	}
	if err := os.MkdirAll(dstAbs, 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dstAbs, "state.json")); err == nil {
		fmt.Println("目标目录里已经存在 state.json，将直接切换配置（不复制）")
	} else {
		entries, err := os.ReadDir(dstAbs)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return errors.New("目标目录不是空目录，请换一个位置")
		}
		fmt.Printf("即将把 %s 复制到 %s\n", from, dstAbs)
		if !*yes {
			fmt.Print("确认继续？输入 yes 回车：")
			var ans string
			fmt.Scanln(&ans)
			if strings.ToLower(strings.TrimSpace(ans)) != "yes" {
				return errors.New("已取消")
			}
		}
		for _, name := range []string{"files", "trash", "audit", "uploads", "cache", "state.json"} {
			src := filepath.Join(from, name)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			dst := filepath.Join(dstAbs, name)
			fmt.Println("  复制 " + name + " …")
			if name == "state.json" {
				if err := copyFileSimple(src, dst); err != nil {
					return err
				}
				continue
			}
			if err := fsops.Copy(src, dst); err != nil {
				return fmt.Errorf("复制 %s 失败：%w", name, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dstAbs, "state.json")); err != nil {
			return errors.New("复制完成但目标目录缺少 state.json，已放弃切换")
		}
	}
	if err := config.SetValue(cfg.Path, "space_dir", dstAbs); err != nil {
		return err
	}
	fmt.Println("空间目录已切换为 " + dstAbs + "，请重启 homevault 生效。")
	fmt.Println("确认服务正常后，可以手动删除旧目录：" + from)
	return nil
}

func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Sync()
}

// ---------------------------------------------------------------------------
// setpass / adduser
// ---------------------------------------------------------------------------

func openStoreForCLI(cfgPath string) (*config.Config, *state.Store, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.EnsureLayout(); err != nil {
		return nil, nil, err
	}
	st, err := state.Open(cfg.StatePath())
	if err != nil {
		return nil, nil, err
	}
	if _, err := st.EnsureSettings(state.DefaultSettings()); err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

func cmdSetPass(args []string) error {
	fl := flag.NewFlagSet("setpass", flag.ContinueOnError)
	cfgPath := fl.String("config", "config.ini", "配置文件路径")
	user := fl.String("user", "", "账号名")
	password := fl.String("password", "", "新密码（留空则随机生成）")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *user == "" {
		return errors.New("必须用 -user 指定账号")
	}
	_, st, err := openStoreForCLI(*cfgPath)
	if err != nil {
		return err
	}
	u, ok := st.User(*user)
	if !ok {
		return fmt.Errorf("账号 %q 不存在", *user)
	}
	pw := *password
	generated := false
	if pw == "" {
		pw = auth.RandomPassword(16)
		generated = true
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	next := u.Clone()
	next.PasswordHash = hash
	if err := st.PutUser(next); err != nil {
		return err
	}
	if generated {
		fmt.Printf("已为 %s 设置随机密码：%s\n", u.Username, pw)
	} else {
		fmt.Printf("已更新 %s 的密码\n", u.Username)
	}
	return nil
}

func cmdAddUser(args []string) error {
	fl := flag.NewFlagSet("adduser", flag.ContinueOnError)
	cfgPath := fl.String("config", "config.ini", "配置文件路径")
	user := fl.String("user", "", "账号名")
	password := fl.String("password", "", "密码")
	display := fl.String("display", "", "显示名")
	root := fl.String("root", "", "存储空间标识（留空则用账号名）")
	quota := fl.String("quota", "", "配额，如 10GiB / 0 表示不限")
	rate := fl.Int("rate", -1, "限速 Mbps（0 表示不限）")
	admin := fl.Bool("admin", false, "创建为管理员（管理员没有存储空间）")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *user == "" || *password == "" {
		return errors.New("必须指定 -user 与 -password")
	}
	if err := state.ValidateUsername(*user); err != nil {
		return err
	}
	cfg, st, err := openStoreForCLI(*cfgPath)
	if err != nil {
		return err
	}
	if _, exists := st.User(*user); exists {
		return fmt.Errorf("账号 %q 已存在", *user)
	}
	settings := st.Settings()
	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}
	nu := &state.User{
		Username: *user, DisplayName: *display, PasswordHash: hash,
		IsAdmin: *admin, Permissions: map[string]bool{}, CreatedAt: time.Now().UTC(),
	}
	if *admin {
		for _, p := range state.AllPermissions {
			nu.Permissions[p] = true
		}
	} else {
		if *root == "" {
			*root = strings.ToLower(*user)
		}
		cleanRoot, err := fsops.CleanRoot(*root)
		if err != nil {
			return err
		}
		nu.Root = cleanRoot
		for _, p := range []string{state.PermRead, state.PermUpload, state.PermModify,
			state.PermDelete, state.PermTrashView, state.PermTrashManage, state.PermLogsView} {
			nu.Permissions[p] = true
		}
		if *quota == "" {
			nu.Quota = int64(settings.DefaultQuota)
		} else {
			size, err := state.ParseSizeString(*quota)
			if err != nil {
				return err
			}
			nu.Quota = int64(size)
		}
		if *rate < 0 {
			nu.RateLimitMbps = settings.DefaultRateLimit
		} else {
			nu.RateLimitMbps = *rate
		}
	}
	if err := st.PutUser(nu); err != nil {
		return err
	}
	if nu.Root != "" {
		ops, err := fsops.New(cfg.FilesPath())
		if err != nil {
			return err
		}
		if _, err := ops.EnsureRoot(nu.Root); err != nil {
			return err
		}
	}
	fmt.Printf("已创建账号 %s（管理员=%v，存储空间=%q，配额=%s）\n",
		nu.Username, nu.IsAdmin, nu.Root, state.FormatSize(nu.Quota))
	return nil
}
