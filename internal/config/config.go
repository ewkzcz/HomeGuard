/**
 * 配置：守护开关、Clash 连接方式、住宅出口分组、受保护的程序与域名、出口国家名单，保存在数据目录的 config.json。
 */
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

/** App：一个受保护的程序，按程序所在路径匹配（正则，(?i) 表示不区分大小写） */
type App struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Patterns []string `json:"patterns"`
}

/** Config：全部设置 */
type Config struct {
	// Enabled：守护开关；关闭后只显示状态，不断开连接也不暂停程序
	Enabled bool `json:"enabled"`
	// PauseApps：线路不安全时暂停受保护的程序，恢复后自动继续
	PauseApps bool `json:"pauseApps"`
	// Controller：Clash 控制接口，unix:套接字路径 或 http://地址:端口
	Controller string `json:"controller"`
	Secret     string `json:"secret"`
	// ResidentialGroup：住宅出口分组名；受保护的连接必须从这个组里的住宅代理出去
	ResidentialGroup string   `json:"residentialGroup"`
	Apps             []App    `json:"apps"`
	Domains          []string `json:"domains"`
	Supported        []string `json:"supported"`
	Unsupported      []string `json:"unsupported"`
	// ProbeInterval：出口核验间隔（秒）；切换住宅代理时会立即核验
	ProbeInterval int    `json:"probeInterval"`
	Port          int    `json:"port"`
	Theme         string `json:"theme"`
}

/** DefaultApps：默认受保护的程序 */
func DefaultApps() []App {
	return []App{
		{ID: "claude", Name: "Claude 桌面端", Patterns: []string{`(?i)/claude\.app/`, `(?i)\\AnthropicClaude\\`}},
		{ID: "claude-code", Name: "Claude Code", Patterns: []string{`(?i)/claude/versions/`, `(?i)/bin/claude$`, `(?i)[\\/]claude\.exe$`}},
		{ID: "codex", Name: "Codex", Patterns: []string{`(?i)/codex\.app/`, `(?i)/codex$`, `(?i)[\\/]codex\.exe$`}},
		{ID: "chatgpt", Name: "ChatGPT", Patterns: []string{`(?i)/chatgpt\.app/`, `(?i)[\\/]chatgpt\.exe$`}},
	}
}

/** Default：默认设置 */
func Default() Config {
	ctl := "unix:/tmp/verge/verge-mihomo.sock"
	if runtime.GOOS == "windows" {
		ctl = "http://127.0.0.1:9097"
	}
	return Config{
		Enabled:          true,
		PauseApps:        true,
		Controller:       ctl,
		ResidentialGroup: "住宅出口",
		Apps:             DefaultApps(),
		Domains:          []string{"anthropic.com", "claude.ai", "claude.com", "claudeusercontent.com", "openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com"},
		Supported:        []string{"US", "CA", "GB", "IE", "DE", "FR", "NL", "SE", "NO", "DK", "FI", "IT", "ES", "PT", "PL", "CZ", "AT", "CH", "BE", "LU", "JP", "KR", "SG", "TW", "AU", "NZ", "IL", "AE", "MX", "BR", "IN", "PH", "TH", "MY", "ID", "VN", "ZA", "TR", "SA", "AR", "CL"},
		Unsupported:      []string{"CN", "HK", "MO", "RU", "IR", "KP", "CU", "SY", "BY", "VE"},
		ProbeInterval:    60,
		Port:             17888,
	}
}

/** Store：读写配置，线程安全 */
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

/** Open：读取配置，文件不存在或缺项时用默认值补齐 */
func Open(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &s.cfg); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	s.fill()
	return s, nil
}

/** fill：补齐缺失的项 */
func (s *Store) fill() {
	d := Default()
	c := &s.cfg
	if c.Controller == "" {
		c.Controller = d.Controller
	}
	if c.ResidentialGroup == "" {
		c.ResidentialGroup = d.ResidentialGroup
	}
	if len(c.Apps) == 0 {
		c.Apps = d.Apps
	}
	if len(c.Domains) == 0 {
		c.Domains = d.Domains
	}
	if len(c.Supported) == 0 {
		c.Supported = d.Supported
	}
	if len(c.Unsupported) == 0 {
		c.Unsupported = d.Unsupported
	}
	if c.ProbeInterval < 15 {
		c.ProbeInterval = d.ProbeInterval
	}
	if c.Port == 0 {
		c.Port = d.Port
	}
}

/** Get：当前配置的副本 */
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.Apps = append([]App(nil), s.cfg.Apps...)
	c.Domains = append([]string(nil), s.cfg.Domains...)
	return c
}

/** Update：修改并原子写入文件 */
func (s *Store) Update(fn func(*Config)) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	fn(&next)
	old := s.cfg
	s.cfg = next
	s.fill()
	if err := s.save(); err != nil {
		s.cfg = old
		return old, err
	}
	return s.cfg, nil
}

/** save：先写临时文件再改名，避免写一半 */
func (s *Store) save() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

/** DataDir：数据目录，macOS 为「应用程序支持/HomeGuard」；环境变量 HOMEGUARD_DATA_DIR 可指定（测试用） */
func DataDir() (string, error) {
	if d := os.Getenv("HOMEGUARD_DATA_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o700)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "HomeGuard")
	return dir, os.MkdirAll(dir, 0o700)
}
