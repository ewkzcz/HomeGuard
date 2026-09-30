/**
 * 自动查找 Clash 控制接口：不同版本、不同客户端（Clash Verge、Mihomo Party、ClashX Meta 等）、服务模式下，
 * 控制接口的位置不一样。按顺序收集候选，逐个试连，第一个能返回版本号的就用它。
 */
package mihomo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

/** Found：找到的控制接口 */
type Found struct {
	Controller string `json:"controller"`
	Secret     string `json:"secret"`
	Source     string `json:"source"`
	Version    string `json:"version"`
}

/** candidate：一个待试的控制接口 */
type candidate struct{ controller, secret, source string }

var (
	unixRe   = regexp.MustCompile(`(?m)^external-controller-unix:\s*['"]?([^'"\s#]+)`)
	tcpRe    = regexp.MustCompile(`(?m)^external-controller:\s*['"]?([^'"\s#]+)`)
	secretRe = regexp.MustCompile(`(?m)^secret:\s*['"]?([^'"\n#]*?)['"]?\s*$`)
)

/** fromConfig：从 Clash 配置文件里读控制接口与密钥 */
func fromConfig(path, source string) []candidate {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := string(b)
	secret := ""
	if m := secretRe.FindStringSubmatch(text); m != nil {
		secret = strings.TrimSpace(m[1])
	}
	var out []candidate
	if m := unixRe.FindStringSubmatch(text); m != nil {
		out = append(out, candidate{"unix:" + m[1], secret, source})
	}
	if m := tcpRe.FindStringSubmatch(text); m != nil && m[1] != "" {
		out = append(out, candidate{tcpURL(m[1]), secret, source})
	}
	return out
}

/** tcpURL：配置里的 127.0.0.1:9097、:9097、0.0.0.0:9097 统一成本机地址 */
func tcpURL(addr string) string {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	if strings.HasPrefix(addr, ":") || strings.HasPrefix(addr, "0.0.0.0:") || strings.HasPrefix(addr, "[::]:") {
		addr = "127.0.0.1:" + addr[strings.LastIndex(addr, ":")+1:]
	}
	return "http://" + addr
}

/** coreArgsRe：从运行中的内核命令行取配置文件；路径可能带空格，读到下一个参数或行尾为止 */
var coreArgsRe = regexp.MustCompile(` -f (.+?)(?: -[a-zA-Z-]+(?: |$)|$)`)

/** fromProcesses：运行中的 mihomo / clash 内核用的配置文件 */
func fromProcesses() []candidate {
	if runtime.GOOS == "windows" {
		return nil
	}
	out, err := exec.Command("/bin/ps", "-axww", "-o", "command=").Output()
	if err != nil {
		return nil
	}
	var list []candidate
	for _, line := range strings.Split(string(out), "\n") {
		low := strings.ToLower(line)
		if !strings.Contains(low, "mihomo") && !strings.Contains(low, "clash") {
			continue
		}
		if !isCore(line) {
			continue
		}
		if m := coreArgsRe.FindStringSubmatch(line); m != nil {
			list = append(list, fromConfig(strings.TrimSpace(m[1]), "运行中的内核配置 "+filepath.Base(m[1]))...)
		}
	}
	return list
}

/** candidates：全部候选，先读配置，再猜常见位置 */
func candidates() []candidate {
	var list []candidate
	// 1、Clash Verge 的运行配置
	if base, err := os.UserConfigDir(); err == nil {
		for _, dir := range []string{"io.github.clash-verge-rev.clash-verge-rev", "clash-verge", "io.github.clash-verge.clash-verge"} {
			for _, f := range []string{"clash-verge.yaml", "config.yaml"} {
				list = append(list, fromConfig(filepath.Join(base, dir, f), "Clash Verge 配置")...)
			}
		}
		list = append(list, fromConfig(filepath.Join(base, "mihomo-party", "work", "config.yaml"), "Mihomo Party 配置")...)
	}
	// 2、运行中的内核
	list = append(list, fromProcesses()...)
	// 3、常见位置
	for _, s := range []string{"/tmp/verge/verge-mihomo.sock", "/tmp/verge-mihomo.sock", "/tmp/mihomo-party.sock", "/tmp/mihomo.sock"} {
		list = append(list, candidate{"unix:" + s, "", "常见位置"})
	}
	// 临时目录里其他像 mihomo 的套接字（名字不在上面的也试一下）
	for _, s := range clashSockets() {
		if strings.Contains(strings.ToLower(filepath.Base(s)), "mihomo") {
			list = append(list, candidate{"unix:" + s, "", "临时目录"})
		}
	}
	for _, a := range []string{"127.0.0.1:9097", "127.0.0.1:9090"} {
		list = append(list, candidate{"http://" + a, "", "常见端口"})
	}
	return list
}

/** Discover：逐个试连，返回第一个可用的控制接口 */
func Discover(ctx context.Context) (Found, bool) {
	seen := map[string]bool{}
	for _, c := range candidates() {
		key := c.controller + "\x00" + c.secret
		if seen[key] {
			continue
		}
		seen[key] = true
		// 套接字不存在时不必试连
		if p, ok := strings.CutPrefix(c.controller, "unix:"); ok {
			if _, err := os.Stat(p); err != nil {
				continue
			}
		}
		tctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		v, err := New(c.controller, c.secret).Version(tctx)
		cancel()
		if err == nil {
			return Found{Controller: c.controller, Secret: c.secret, Source: c.source, Version: v}, true
		}
	}
	return Found{}, false
}

/** Attempt：诊断时一个候选的结果 */
type Attempt struct {
	Controller string `json:"controller"`
	Source     string `json:"source"`
	HasSecret  bool   `json:"hasSecret"`
	Result     string `json:"result"`
	OK         bool   `json:"ok"`
}

/** Diagnosis：诊断结果，供界面展示，帮助定位找不到 Clash 的原因 */
type Diagnosis struct {
	Attempts []Attempt `json:"attempts"`
	Cores    []string  `json:"cores"`
	Sockets  []string  `json:"sockets"`
}

/** Diagnose：试连全部候选并记录每一个的结果，同时列出运行中的内核与临时目录里的套接字 */
func Diagnose(ctx context.Context) Diagnosis {
	var d Diagnosis
	seen := map[string]bool{}
	for _, c := range candidates() {
		key := c.controller + "\x00" + c.secret
		if seen[key] {
			continue
		}
		seen[key] = true
		a := Attempt{Controller: c.controller, Source: c.source, HasSecret: c.secret != ""}
		if p, ok := strings.CutPrefix(c.controller, "unix:"); ok {
			if _, err := os.Stat(p); err != nil {
				a.Result = "文件不存在"
				d.Attempts = append(d.Attempts, a)
				continue
			}
		}
		tctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		v, err := New(c.controller, c.secret).Version(tctx)
		cancel()
		if err == nil {
			a.OK, a.Result = true, "可用 · "+v
		} else {
			a.Result = err.Error()
		}
		d.Attempts = append(d.Attempts, a)
	}
	// 运行中的内核：只看程序名是 Clash 内核的进程，不列其他命令（其他命令的参数里可能有密钥）
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("/bin/ps", "-axww", "-o", "user=,command=").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if isCore(line) {
					d.Cores = append(d.Cores, strings.TrimSpace(line))
				}
			}
		}
	}
	d.Sockets = clashSockets()
	return d
}

/** isCore：命令行的程序名是 mihomo / clash 内核（参数里提到 clash 的其他命令不算） */
func isCore(line string) bool {
	exe := line
	if i := strings.Index(exe, " -"); i >= 0 {
		exe = exe[:i]
	}
	base := strings.ToLower(filepath.Base(strings.TrimSpace(exe)))
	return strings.Contains(base, "mihomo") || strings.Contains(base, "clash")
}

/** clashSockets：临时目录里像 Clash 的套接字 */
func clashSockets() []string {
	var out []string
	for _, pat := range []string{"/tmp/*.sock", "/tmp/*/*.sock"} {
		m, _ := filepath.Glob(pat)
		for _, p := range m {
			low := strings.ToLower(p)
			if strings.Contains(low, "mihomo") || strings.Contains(low, "clash") || strings.Contains(low, "verge") {
				out = append(out, p)
			}
		}
	}
	return out
}
