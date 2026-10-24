package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SizeValue 是"字节数"的 JSON 表示：既接受数字 10737418240，也接受 "10GiB" / "500MB"。
type SizeValue int64

// Bytes 返回值本身。
func (s SizeValue) Bytes() int64 { return int64(s) }

// UnmarshalJSON 实现数字与带单位字符串两种写法。
func (s *SizeValue) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "null" || raw == `""` {
		return errors.New("容量值不能为空（不限请显式写 0）")
	}
	if strings.HasPrefix(raw, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		n, err := ParseSizeString(str)
		if err != nil {
			return err
		}
		*s = SizeValue(n)
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("无法解析容量 %s", raw)
	}
	if n < 0 {
		return errors.New("容量不能为负")
	}
	*s = SizeValue(n)
	return nil
}

// ParseSizeString 解析 "10GiB" / "500 MB" / "0" / "unlimited"；无法解析时报错，不退化成 0。
func ParseSizeString(v string) (int64, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0, errors.New("容量值不能为空（不限请显式写 0）")
	}
	if strings.EqualFold(s, "unlimited") {
		return 0, nil
	}
	units := []struct {
		suffix string
		mul    int64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
		{"kb", 1 << 10}, {"mb", 1 << 20}, {"gb", 1 << 30}, {"tb", 1 << 40},
		{"b", 1},
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	numPart := strings.TrimSpace(s[:i])
	unitPart := strings.ToLower(strings.TrimSpace(s[i:]))
	if numPart == "" {
		return 0, fmt.Errorf("无法解析容量 %q", v)
	}
	num, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("无法解析容量 %q: %w", v, err)
	}
	if num < 0 {
		return 0, fmt.Errorf("容量不能为负：%q", v)
	}
	if unitPart == "" {
		return int64(num), nil
	}
	for _, u := range units {
		if unitPart == u.suffix {
			return int64(num * float64(u.mul)), nil
		}
	}
	return 0, fmt.Errorf("未知容量单位 %q", unitPart)
}

// ParseDurationString 解析 "720h" / "30d" / "2w" / "0"。
func ParseDurationString(v string) (time.Duration, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0, errors.New("时长不能为空（0 表示禁用）")
	}
	if s == "0" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return 0, errors.New("时长不能为负")
		}
		return d, nil
	}
	total := time.Duration(0)
	rest := s
	for rest != "" {
		i := 0
		for i < len(rest) && (rest[i] >= '0' && rest[i] <= '9' || rest[i] == '.') {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("无法解析时长 %q", v)
		}
		num, err := strconv.ParseFloat(rest[:i], 64)
		if err != nil {
			return 0, fmt.Errorf("无法解析时长 %q: %w", v, err)
		}
		rest = rest[i:]
		unit := ""
		for j := 0; j < len(rest); j++ {
			if rest[j] >= '0' && rest[j] <= '9' || rest[j] == '.' {
				break
			}
			unit += string(rest[j])
		}
		rest = rest[len(unit):]
		switch strings.ToLower(unit) {
		case "d":
			total += time.Duration(num * float64(24*time.Hour))
		case "w":
			total += time.Duration(num * float64(7*24*time.Hour))
		case "h":
			total += time.Duration(num * float64(time.Hour))
		case "m":
			total += time.Duration(num * float64(time.Minute))
		case "s":
			total += time.Duration(num * float64(time.Second))
		default:
			return 0, fmt.Errorf("无法解析时长 %q（未知单位 %q）", v, unit)
		}
	}
	return total, nil
}

// FormatDurationString 把时长格式化为 API 中使用的字符串。
func FormatDurationString(d time.Duration) string { return d.String() }

// FormatSize 把字节数格式化为人类可读字符串（KiB/MiB/GiB…）。
func FormatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < len(units)-1 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %s", float64(n)/float64(div), units[exp])
}

// Settings 是可以在管理页面里改、立即生效并持久化的运行期参数。
//
// 设计立场：部署时可以完全不带配置文件；只有监听地址、数据目录、TLS 这类
// "改了就换进程形态"的参数留在 config.ini 里（页面只读展示）。
type Settings struct {
	// SpaceDir 为空表示沿用配置文件/系统默认值；非空时为**待生效**的新空间目录，需重启。
	SpaceDir string `json:"spaceDir"`
	// Listen / TLSCert / TLSKey 也是**待生效**的引导配置：管理页面改完会写回
	// config.ini，重启后生效。为空表示与当前配置文件一致。
	Listen  string `json:"listen,omitempty"`
	TLSCert string `json:"tlsCert,omitempty"`
	TLSKey  string `json:"tlsKey,omitempty"`
	// SpaceQuota 是整个空间目录的总容量上限（所有用户 + 回收站 + 未提交分片之和）。
	// 0 = 不限（等于"有多少磁盘用多少"），默认 5 GiB。
	SpaceQuota        SizeValue `json:"spaceQuota"`
	DefaultQuota      SizeValue `json:"defaultQuota"`         // 新建用户的默认配额，0 = 不限
	DefaultRateLimit  int       `json:"defaultRateLimitMbps"` // 新建用户的默认限速（Mbps），0 = 不限
	TrashRetention    string    `json:"trashRetention"`       // "720h" / "30d"，"0" = 永不自动清理
	LogRetention      string    `json:"logRetention"`         // 审计日志保留时长
	UploadSessionTTL  string    `json:"uploadSessionTTL"`     // 未完成分片上传会话的过期时间
	MaxUploadSize     SizeValue `json:"maxUploadSize"`        // 单文件上限，0 = 不限
	ChunkSize         SizeValue `json:"chunkSize"`            // 分片大小
	SessionTTL        string    `json:"sessionTTL"`           // 登录会话有效期
	TrustProxy        bool      `json:"trustProxy"`           // 是否采信反向代理传来的客户端 IP
	AccessLogDisabled bool      `json:"accessLogDisabled"`    // 是否关闭 HTTP 访问日志
	OfficePreview     string    `json:"officePreview"`        // "auto" | "off" | "builtin"
}

// DefaultSettings 返回内置默认值。
func DefaultSettings() Settings {
	return Settings{
		SpaceDir:          "",
		SpaceQuota:        5 << 30,
		DefaultQuota:      1 << 30,
		DefaultRateLimit:  0,
		TrashRetention:    "720h",
		LogRetention:      "2160h",
		UploadSessionTTL:  "24h",
		MaxUploadSize:     0,
		ChunkSize:         8 << 20,
		SessionTTL:        "168h",
		TrustProxy:        false,
		AccessLogDisabled: false,
		OfficePreview:     "auto",
	}
}

// normalized 修正历史数据里不自洽的组合（例如旧版本默认配额 10GiB 大于新的空间总容量）。
func (s Settings) normalized() Settings {
	if s.SpaceQuota > 0 && (s.DefaultQuota == 0 || s.DefaultQuota > s.SpaceQuota) {
		s.DefaultQuota = s.SpaceQuota
	}
	if s.ChunkSize == 0 {
		s.ChunkSize = 8 << 20
	}
	return s
}

// TrashDur / LogDur / UploadSessionDur / SessionDur 解析对应时长；已通过 Validate，故不会失败。
func (s Settings) TrashDur() time.Duration {
	d, _ := ParseDurationString(s.TrashRetention)
	return d
}

func (s Settings) LogDur() time.Duration {
	d, _ := ParseDurationString(s.LogRetention)
	return d
}

func (s Settings) UploadSessionDur() time.Duration {
	d, _ := ParseDurationString(s.UploadSessionTTL)
	return d
}

func (s Settings) SessionDur() time.Duration {
	d, _ := ParseDurationString(s.SessionTTL)
	return d
}

// Validate 校验设置项；错误消息直接面向管理页面，需指明字段与原因。
func (s Settings) Validate() error {
	if s.SpaceQuota < 0 {
		return errors.New("spaceQuota 不能为负数（0 = 不限）")
	}
	if s.DefaultQuota < 0 {
		return errors.New("defaultQuota 不能为负数")
	}
	if s.SpaceQuota > 0 && s.DefaultQuota > s.SpaceQuota {
		return fmt.Errorf("defaultQuota (%s) 不能超过空间总容量 spaceQuota (%s)",
			FormatSize(int64(s.DefaultQuota)), FormatSize(int64(s.SpaceQuota)))
	}
	if s.DefaultRateLimit < 0 {
		return errors.New("defaultRateLimitMbps 不能为负数")
	}
	if s.SpaceDir != "" && !filepath.IsAbs(s.SpaceDir) {
		return fmt.Errorf("spaceDir 必须是绝对路径，当前为 %q", s.SpaceDir)
	}
	switch s.OfficePreview {
	case "", "auto", "off", "builtin":
	default:
		return fmt.Errorf("officePreview 只能是 auto / off / builtin，当前为 %q", s.OfficePreview)
	}
	if s.MaxUploadSize < 0 {
		return errors.New("maxUploadSize 不能为负数")
	}
	if s.ChunkSize < 64<<10 {
		return fmt.Errorf("chunkSize 至少 64KiB，当前 %d", int64(s.ChunkSize))
	}
	if s.ChunkSize > 1<<30 {
		return fmt.Errorf("chunkSize 过大（上限 1GiB），当前 %d", int64(s.ChunkSize))
	}
	if s.MaxUploadSize > 0 && int64(s.MaxUploadSize) < int64(s.ChunkSize) {
		return fmt.Errorf("maxUploadSize (%d) 不能小于 chunkSize (%d)", int64(s.MaxUploadSize), int64(s.ChunkSize))
	}
	if d, err := ParseDurationString(s.TrashRetention); err != nil {
		return fmt.Errorf("trashRetention: %w", err)
	} else if d < 0 {
		return errors.New("trashRetention 不能为负")
	}
	if d, err := ParseDurationString(s.LogRetention); err != nil {
		return fmt.Errorf("logRetention: %w", err)
	} else if d < 0 {
		return errors.New("logRetention 不能为负")
	}
	if d, err := ParseDurationString(s.UploadSessionTTL); err != nil {
		return fmt.Errorf("uploadSessionTTL: %w", err)
	} else if d <= 0 {
		return errors.New("uploadSessionTTL 必须大于 0")
	}
	if d, err := ParseDurationString(s.SessionTTL); err != nil {
		return fmt.Errorf("sessionTTL: %w", err)
	} else if d <= 0 {
		return errors.New("sessionTTL 必须大于 0")
	}
	return nil
}

// Settings 返回当前运行期设置的拷贝。
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.Settings == nil {
		return DefaultSettings()
	}
	return s.data.Settings.normalized()
}

// EnsureSettings 在首次运行时写入默认设置，返回最终生效值。
func (s *Store) EnsureSettings(def Settings) (Settings, error) {
	def = def.normalized()
	if err := def.Validate(); err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Settings == nil {
		c := def
		s.data.Settings = &c
		if err := s.saveLocked(); err != nil {
			return Settings{}, err
		}
	}
	return *s.data.Settings, nil
}

// PutSettings 校验并持久化新的运行期设置。
func (s *Store) PutSettings(next Settings) (Settings, error) {
	if err := next.Validate(); err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := next
	s.data.Settings = &c
	if err := s.saveLocked(); err != nil {
		return Settings{}, err
	}
	return c, nil
}
