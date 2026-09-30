/**
 * 体检：按出口、DNS、本机画像、稳定性给当前环境打分，指出问题和改法。
 * 体检只供参考，不决定放行；放行由守护引擎实时判断。
 */
package checkup

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"homeguard/internal/config"
	"homeguard/internal/guard"
	"homeguard/internal/probe"
)

/** Item：一项检查 */
type Item struct {
	Cat    string `json:"cat"`
	Name   string `json:"name"`
	Weight int    `json:"weight"`
	Score  int    `json:"score"`
	Value  string `json:"value"`
	Tip    string `json:"tip,omitempty"`
}

/** Report：体检结果 */
type Report struct {
	Score int       `json:"score"`
	Items []Item    `json:"items"`
	At    time.Time `json:"at"`
}

/** cnDNS：常见国内公共 DNS */
var cnDNS = map[string]bool{"114.114.114.114": true, "114.114.115.115": true, "223.5.5.5": true, "223.6.6.6": true, "119.29.29.29": true, "182.254.116.116": true, "180.76.76.76": true, "117.50.10.10": true, "1.2.4.8": true, "210.2.4.8": true}

/**
 * Run：体检一次
 *
 * 处理流程：
 * 1、并行核验出口与几个网站的可达性
 * 2、守护条件、出口、DNS、本机、稳定性逐项打分
 * 3、按权重算总分
 */
func Run(ctx context.Context, snap guard.Snapshot, cfg config.Config, history []guard.ExitRecord, browser *Browser) Report {
	// 1、网络探测
	var (
		wg                sync.WaitGroup
		x                 probe.Exit
		google, web, site int
	)
	wg.Add(4)
	go func() { defer wg.Done(); x = probe.Run(ctx) }()
	go func() { defer wg.Done(); google = probe.Status(ctx, "https://www.google.com/generate_204") }()
	// 测 robots.txt：主页对非浏览器请求一律返回人机验证，不能代表地区是否可达
	go func() { defer wg.Done(); web = probe.Status(ctx, "https://claude.ai/robots.txt") }()
	go func() { defer wg.Done(); site = probe.Status(ctx, "https://www.anthropic.com/robots.txt") }()
	wg.Wait()
	if !x.OK() {
		x = snap.Exit
	}

	var items []Item
	add := func(cat, name string, w, score int, value, tip string) {
		items = append(items, Item{Cat: cat, Name: name, Weight: w, Score: score, Value: value, Tip: tip})
	}

	// 2、守护条件
	for _, c := range snap.Checks {
		if !c.Critical {
			continue
		}
		add("守护", c.Name, 4, map[bool]int{true: 100, false: 0}[c.OK], c.Detail, c.Fix)
	}

	// 出口
	switch {
	case !x.OK():
		add("出口", "出口国家", 14, 50, "未知", "IP 情报接口不可达，检查网络后重新体检")
	case has(cfg.Unsupported, x.Country):
		add("出口", "出口国家", 14, 0, x.Country+" 不支持", "换美国、日本、新加坡等支持地区的住宅代理，并长期固定")
	case has(cfg.Supported, x.Country):
		add("出口", "出口国家", 14, 100, strings.TrimSpace(x.Country+" "+x.City)+" · "+x.IP, "")
	default:
		add("出口", "出口国家", 14, 66, x.Country+" 支持情况未知", "建议改用美国、日本、新加坡等地区")
	}
	switch {
	case x.Country == "" || x.Country2 == "":
		add("出口", "多源情报一致", 3, 50, "数据不足", "")
	case strings.EqualFold(x.Country, x.Country2):
		add("出口", "多源情报一致", 3, 100, x.Country+" = "+x.Country2, "")
	default:
		add("出口", "多源情报一致", 3, 0, x.Country+" ≠ "+x.Country2, "两家情报库判定不一致，这个出口容易被识别")
	}
	switch {
	case !x.OK():
		add("出口", "IP 类型", 6, 50, "未知", "")
	case x.Hosting:
		add("出口", "IP 类型", 6, 30, "机房 IP · "+x.Org, "机房 IP 容易被风控，换真正的住宅 IP")
	case x.Proxy:
		add("出口", "IP 类型", 6, 50, "被标记为代理 · "+x.Org, "换一个没被标记的住宅 IP")
	default:
		add("出口", "IP 类型", 6, 100, "住宅 · "+x.Org, "")
	}
	add("出口", "公共网络", 5, codeScore(google, 204), codeText(google), "检查基础节点是否可用")
	add("出口", "claude.ai 可达", 3, codeScore(web, 200), codeText(web), "")
	add("出口", "anthropic.com 可达", 2, codeScore(site, 200), codeText(site), "")

	// DNS
	add3 := func(name string, w int, s int, v, tip string) { add("DNS", name, w, s, v, tip) }
	ip := lookup(ctx, "claude.ai")
	switch {
	case ip == "":
		add3("claude.ai 解析", 6, 20, "解析失败", "让 Clash 接管 DNS（fake-ip）")
	case strings.HasPrefix(ip, "198.18.") || strings.HasPrefix(ip, "198.19."):
		add3("claude.ai 解析", 6, 100, "Clash 接管（fake-ip）", "")
	case strings.HasPrefix(ip, "160.79.104.") || strings.HasPrefix(ip, "160.79.105.") || strings.HasPrefix(ip, "104.") || strings.HasPrefix(ip, "172.6"):
		add3("claude.ai 解析", 6, 90, "正常 · "+ip, "建议让 Clash 接管 DNS（fake-ip），本机不再发出 DNS 查询")
	default:
		add3("claude.ai 解析", 6, 0, "可疑 · "+ip, "DNS 可能被污染，让 Clash 接管 DNS（fake-ip）")
	}
	if rr, fake, ok := clashDNS(); ok {
		switch {
		case rr && fake:
			add3("DNS 查询走代理", 4, 100, "fake-ip · 按规则走代理", "")
		case fake:
			add3("DNS 查询走代理", 4, 60, "fake-ip，但 DNS 查询不按规则走", "使用分流脚本（会开启 respect-rules）")
		default:
			add3("DNS 查询走代理", 4, 40, "没开 fake-ip", "使用分流脚本")
		}
	}
	// 系统 DNS 是否被接管：直接向系统设置的 DNS 服务器发查询，由 Clash 回答假地址说明查询根本没到达那台服务器
	if servers := systemDNS(); len(servers) > 0 {
		sv := servers[0]
		got := lookupVia(ctx, sv, "claude.ai")
		switch {
		case isFakeIP(got):
			add3("系统 DNS 接管", 4, 100, "发往 "+sv+" 的查询由 Clash 回答，没有到达这台服务器", "")
		case got == "":
			add3("系统 DNS 接管", 4, 50, "向 "+sv+" 查询没有回应", "")
		case cnDNS[sv]:
			add3("系统 DNS 接管", 4, 0, "查询直接到达 "+sv+"（国内），没被 Clash 接管", "打开 Clash 的 TUN 模式并开启 DNS 劫持")
		default:
			add3("系统 DNS 接管", 4, 40, "查询直接到达 "+sv+"，没被 Clash 接管", "打开 Clash 的 TUN 模式并开启 DNS 劫持")
		}
	}
	// DNS 出口：网站那边看到的解析服务器是谁、在哪个国家
	if ip := dnsEgress(ctx); ip != "" {
		g := probe.Lookup(ctx, ip)
		v := strings.TrimSpace(ip + " · " + g.Country + " " + g.Org)
		switch {
		case g.Country == "":
			add3("DNS 出口", 4, 60, v, "")
		case strings.EqualFold(g.Country, "CN"):
			add3("DNS 出口", 4, 0, v+"（国内）", "DNS 查询从国内出去了，检查分流脚本的 DNS 设置")
		default:
			add3("DNS 出口", 4, 100, v, "")
		}
	} else {
		add3("DNS 出口", 4, 50, "测不到", "")
	}

	// 本机画像
	tz := probe.SystemTZ()
	switch {
	case x.Timezone == "" || tz == "":
		add("本机", "系统时区", 5, 50, orUnknown(tz), "")
	case tz == x.Timezone:
		add("本机", "系统时区", 5, 100, tz, "")
	case probe.SameOffset(tz, x.Timezone):
		add("本机", "系统时区", 5, 80, tz+"（与出口 "+x.Timezone+" 时差相同）", "")
	default:
		add("本机", "系统时区", 5, 0, tz+" ≠ 出口 "+x.Timezone, "sudo systemsetup -settimezone "+x.Timezone)
	}
	if loc := defaultsRead("AppleLocale"); loc != "" && x.Country != "" {
		cc := loc[strings.LastIndex(loc, "_")+1:]
		if strings.EqualFold(cc, x.Country) {
			add("本机", "系统地区", 2, 100, loc, "")
		} else {
			add("本机", "系统地区", 2, 60, loc+"（出口在 "+x.Country+"）", "系统设置 → 通用 → 语言与地区，把地区改成出口所在国家")
		}
	}

	// 稳定性
	n := 0
	cut := time.Now().Add(-24 * time.Hour)
	for _, r := range history {
		if r.At.After(cut) {
			n++
		}
	}
	switch {
	case n <= 1:
		add("稳定", "24 小时出口变化", 5, 100, "没有变化", "")
	case n <= 3:
		add("稳定", "24 小时出口变化", 5, 60, strconv.Itoa(n-1)+" 次", "固定使用同一个住宅代理，不要频繁切换")
	default:
		add("稳定", "24 小时出口变化", 5, 20, strconv.Itoa(n-1)+" 次", "出口频繁变化容易触发风控，固定使用同一个住宅代理")
	}

	// 浏览器侧
	addBrowser(ctx, add, browser, x)

	// 3、总分
	sum, total := 0, 0
	for _, it := range items {
		sum += it.Weight * it.Score
		total += it.Weight
	}
	r := Report{Items: items, At: time.Now()}
	if total > 0 {
		r.Score = sum / total
	}
	return r
}

func has(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}

/** codeScore：状态码打分，ok 为期望的状态码 */
func codeScore(code, ok int) int {
	switch {
	case code == ok || code == 200 || code == 204 || code == 301 || code == 302 || code == 307:
		return 100
	case code == 403:
		return 20
	case code == 0:
		return 0
	default:
		return 60
	}
}

func codeText(code int) string {
	switch code {
	case 0:
		return "连不上"
	case 403:
		return "HTTP 403 访问被拒绝"
	default:
		return "HTTP " + strconv.Itoa(code)
	}
}

/** isFakeIP：Clash 的假地址段 198.18.0.0/15 */
func isFakeIP(ip string) bool {
	return strings.HasPrefix(ip, "198.18.") || strings.HasPrefix(ip, "198.19.")
}

/** resolverVia：直接向指定 DNS 服务器查询的解析器（不走系统缓存） */
func resolverVia(server string) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
	}}
}

/** lookupVia：向指定 DNS 服务器查询，返回第一个 IPv4 */
func lookupVia(ctx context.Context, server, host string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := resolverVia(server).LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0].String()
}

/**
 * dnsEgress：DNS 出口，也就是替本机向网站查询的解析服务器的公网 IP
 *
 * 查询谷歌的 o-o.myaddr.l.google.com 文本记录，它返回来问它的那台解析服务器的地址；
 * 查询发给系统 DNS，被 Clash 接管后按规则转给上游，测到的就是实际生效的出口
 */
func dnsEgress(ctx context.Context) string {
	servers := systemDNS()
	if len(servers) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	txt, err := resolverVia(servers[0]).LookupTXT(ctx, "o-o.myaddr.l.google.com")
	if err != nil {
		return ""
	}
	for _, t := range txt {
		if ip := net.ParseIP(strings.TrimSpace(t)); ip != nil {
			return ip.String()
		}
	}
	return ""
}

/** lookup：用系统解析器解析，返回第一个 IPv4 */
func lookup(ctx context.Context, host string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0].String()
}

/** clashDNS：读取 Clash Verge 实际运行的配置，看 DNS 是否 fake-ip 且按规则走代理 */
func clashDNS() (respectRules, fakeIP, ok bool) {
	base, err := os.UserConfigDir()
	if err != nil {
		return
	}
	b, err := os.ReadFile(filepath.Join(base, "io.github.clash-verge-rev.clash-verge-rev", "clash-verge.yaml"))
	if err != nil {
		return
	}
	// 只取顶层 dns: 这一段
	text := "\n" + string(b)
	i := strings.Index(text, "\ndns:")
	if i < 0 {
		return false, false, true
	}
	block := text[i+5:]
	if j := regexp.MustCompile(`\n[A-Za-z]`).FindStringIndex(block); j != nil {
		block = block[:j[0]]
	}
	return regexp.MustCompile(`respect-rules:\s*true`).MatchString(block), regexp.MustCompile(`enhanced-mode:\s*fake-ip`).MatchString(block), true
}

/** systemDNS：系统配置的 DNS 服务器 */
func systemDNS() []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	out, err := exec.Command("/usr/sbin/scutil", "--dns").Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var list []string
	for _, m := range regexp.MustCompile(`nameserver\[\d+\]\s*:\s*(\S+)`).FindAllStringSubmatch(string(out), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			list = append(list, m[1])
		}
	}
	if len(list) > 3 {
		list = list[:3]
	}
	return list
}

/** defaultsRead：读取系统偏好设置 */
func defaultsRead(key string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("/usr/bin/defaults", "read", "-g", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
