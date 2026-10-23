// Package config 处理 homevault 的**引导配置**。
//
// 设计立场（部署零配置 + 不允许空值）：
//   - 配置文件里只保留"改了就换进程形态"的必须项：监听地址、空间目录、TLS 证书。
//   - 每一项都有非空的最小默认值，运行时缺省即可启动，不需要先写配置。
//   - 其余参数（配额、保留期、限速、分片大小……）属于运行期设置，
//     存在 <空间目录>/state.json 里，可在管理页面修改并立即生效。
//   - 任何非法值都让进程**启动失败并打印原因**，绝不静默退化成 0 或空串。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config 是引导配置。
type Config struct {
	// Listen 是 HTTP(S) 监听地址。
	Listen string
	// SpaceDir 是空间目录：files/ trash/ audit/ uploads/ cache/ state.json 都在其中。
	// 整体可搬迁，便于换盘或迁移到更大的磁盘。
	SpaceDir string
	// TLSCert / TLSKey 同时非空时启用内置 HTTPS；否则建议交给反向代理。
	TLSCert string
	TLSKey  string
	// AdminUser / AdminPassword 仅在**首次启动创建管理员**时读取。
	// 默认 admin / admin，并在管理界面强制要求修改后才能使用其他功能。
	AdminUser     string
	AdminPassword string
	// Path 是配置文件自身的路径。
	Path string
}

// DefaultSpaceDir 返回按操作系统约定的默认空间目录。
func DefaultSpaceDir() string {
	if runtime.GOOS == "windows" {
		return `C:\HomeVaultSpace`
	}
	return "/root/HomeVaultSpace"
}

// Default 返回内置默认配置。
func Default() *Config {
	return &Config{
		Listen:        "0.0.0.0:8080",
		SpaceDir:      DefaultSpaceDir(),
		AdminUser:     "admin",
		AdminPassword: "admin",
	}
}

// Template 是首次运行时写出的模板（每一项都是可直接使用的非空值）。
const Template = `# homevault 引导配置
# 绝大多数参数不需要在这里配置：启动后在管理页面修改并立即生效。
# 这里只放"改了就换进程形态"的必须项。
# 含 # 的值请用双引号包起来。改完需要重启进程。

# HTTP 监听地址。放在反向代理后面时建议改成 127.0.0.1:8080。
listen = "0.0.0.0:8080"

# 空间目录：用户文件、回收站、审计日志、上传分片、缓存与元数据都在其中。
# 目录整体可搬迁：换盘时改这里 + 执行 homevault migrate-space。
space_dir = ""

# 同时设置下面两项才启用内置 HTTPS；否则建议由反向代理（Caddy / Nginx）终结 TLS。
tls_cert = ""
tls_key = ""

# 首次启动创建管理员用。默认 admin / admin，登录后会强制要求改账号或密码。
# 只有第一次启动（空间目录里还没有 state.json）时读取这两项。
admin_user = "admin"
admin_password = "admin"
`

// Load 读取配置文件；不存在时写入模板并返回默认配置。
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	cfg.Path = abs

	raw, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(abs, []byte(Template), 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if err := cfg.parse(string(raw)); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}

	base := filepath.Dir(abs)
	if err := cfg.finalize(base); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	return cfg, nil
}

// finalize 解析相对路径并校验。
func (c *Config) finalize(base string) error {
	if strings.TrimSpace(c.SpaceDir) == "" {
		c.SpaceDir = DefaultSpaceDir()
	}
	c.SpaceDir = absFrom(base, c.SpaceDir)
	if c.TLSCert != "" {
		c.TLSCert = absFrom(base, c.TLSCert)
	}
	if c.TLSKey != "" {
		c.TLSKey = absFrom(base, c.TLSKey)
	}
	if c.AdminUser == "" {
		c.AdminUser = "admin"
	}
	if c.AdminPassword == "" {
		c.AdminPassword = "admin"
	}
	return c.Validate()
}

func absFrom(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

func (c *Config) parse(text string) error {
	for i, line := range strings.Split(text, "\n") {
		lineNo := i + 1
		s := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}
		if strings.HasPrefix(s, "[") {
			return fmt.Errorf("第 %d 行：不支持分节，请使用扁平的 key = value", lineNo)
		}
		eq := strings.Index(s, "=")
		if eq < 0 {
			return fmt.Errorf("第 %d 行：缺少 '='：%q", lineNo, s)
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		val := strings.TrimSpace(s[eq+1:])
		if strings.HasPrefix(val, `"`) {
			// 允许 "值" # 注释 这种写法：以第二个引号作为值的结束。
			end := strings.Index(val[1:], `"`)
			if end < 0 {
				return fmt.Errorf("第 %d 行：引号未闭合", lineNo)
			}
			end++ // 转回 val 的下标
			quoted := val[1:end]
			if rest := strings.TrimSpace(val[end+1:]); rest != "" &&
				!strings.HasPrefix(rest, "#") && !strings.HasPrefix(rest, ";") {
				return fmt.Errorf("第 %d 行：引号后的内容无法识别：%q", lineNo, rest)
			}
			val = quoted
		} else if idx := strings.IndexAny(val, "#;"); idx >= 0 {
			val = strings.TrimSpace(val[:idx])
		}
		switch key {
		case "listen":
			if val == "" {
				return fmt.Errorf("第 %d 行：listen 不能为空", lineNo)
			}
			c.Listen = val
		case "space_dir":
			c.SpaceDir = val // 允许留空 = 使用系统默认
		case "tls_cert":
			c.TLSCert = val
		case "tls_key":
			c.TLSKey = val
		case "admin_user":
			if val == "" {
				return fmt.Errorf("第 %d 行：admin_user 不能为空", lineNo)
			}
			c.AdminUser = val
		case "admin_password":
			c.AdminPassword = val
		default:
			return fmt.Errorf("第 %d 行：未知配置项 %q（运行期参数请在管理页面配置）", lineNo, key)
		}
	}
	return nil
}

// Validate 校验引导配置。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("listen 不能为空")
	}
	if strings.TrimSpace(c.SpaceDir) == "" {
		return errors.New("space_dir 不能为空（留空会使用系统默认目录）")
	}
	if !filepath.IsAbs(c.SpaceDir) {
		return fmt.Errorf("space_dir 必须是绝对路径，当前为 %q", c.SpaceDir)
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls_cert 与 tls_key 必须同时设置或同时留空")
	}
	return nil
}

// TLSEnabled 表示是否启用内置 HTTPS。
func (c *Config) TLSEnabled() bool { return c.TLSCert != "" && c.TLSKey != "" }

// ---------------------------------------------------------------------------
// 空间目录布局
// ---------------------------------------------------------------------------

// FilesPath 返回用户文件根目录。
func (c *Config) FilesPath() string { return filepath.Join(c.SpaceDir, "files") }

// TrashPath 返回回收站数据目录。
func (c *Config) TrashPath() string { return filepath.Join(c.SpaceDir, "trash") }

// UploadPath 返回分片上传临时目录。
func (c *Config) UploadPath() string { return filepath.Join(c.SpaceDir, "uploads") }

// AuditPath 返回审计日志目录。
func (c *Config) AuditPath() string { return filepath.Join(c.SpaceDir, "audit") }

// CachePath 返回预览等派生产物缓存目录。
func (c *Config) CachePath() string { return filepath.Join(c.SpaceDir, "cache") }

// StatePath 返回元数据文件路径。
func (c *Config) StatePath() string { return filepath.Join(c.SpaceDir, "state.json") }

// EnsureLayout 创建空间目录骨架。
func (c *Config) EnsureLayout() error {
	for _, dir := range []string{c.SpaceDir, c.FilesPath(), c.TrashPath(), c.UploadPath(), c.AuditPath(), c.CachePath()} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return nil
}

// HasState 报告空间目录里是否已经有元数据（用于判断是否首次启动）。
func (c *Config) HasState() bool {
	_, err := os.Stat(c.StatePath())
	return err == nil
}

// SetValue 就地改写配置文件中的某一项（保留其他内容与注释）。
// 用于"管理页面修改了需要重启的参数"这一场景。
func SetValue(path, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(trimmed[:eq]), key) {
			// 保留配置文件里原有的大小写，避免把 listen 改写成 LISTEN。
			existing := strings.TrimSpace(trimmed[:eq])
			lines[i] = fmt.Sprintf("%s = %q", existing, value)
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, fmt.Sprintf("%s = %q", key, value))
	}
	out := strings.Join(lines, "\n")
	return os.WriteFile(path, []byte(out), 0o600)
}
