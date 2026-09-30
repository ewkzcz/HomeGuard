/**
 * 浏览器侧检测：打开默认浏览器采集 WebRTC 候选地址、浏览器时区、语言、区域、渲染器、平台，与出口对比打分。
 * 这些信号只能在浏览器里拿到；只用于体检，不参与拦截。
 */
package checkup

import (
	"context"
	"net"
	"runtime"
	"strings"
	"time"

	"homeguard/internal/probe"
)

/** Browser：浏览器上报的结果 */
type Browser struct {
	TZ             string    `json:"tz"`
	Locale         string    `json:"locale"`
	Langs          []string  `json:"langs"`
	RTCSrflx       []string  `json:"rtcSrflx"`
	RTCHost        []string  `json:"rtcHost"`
	WebGL          string    `json:"webgl"`
	UAPlatform     string    `json:"uaPlatform"`
	AcceptLanguage string    `json:"acceptLanguage"`
	UserAgent      string    `json:"userAgent"`
	At             time.Time `json:"at"`
}

/** regionOf：语言标签里的地区，如 zh-CN → CN，en → 空 */
func regionOf(tag string) string {
	tag = strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	parts := strings.Split(tag, "-")
	for _, p := range parts[1:] {
		if len(p) == 2 {
			return strings.ToUpper(p)
		}
	}
	return ""
}

/** zhRegions：使用中文合理的出口地区 */
var zhRegions = map[string]bool{"CN": true, "TW": true, "HK": true, "MO": true, "SG": true}

/** publicIP：公网地址（mDNS 名称、内网地址不算） */
func publicIP(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip)
}

var cgnat = func() *net.IPNet { _, n, _ := net.ParseCIDR("100.64.0.0/10"); return n }()

/** addBrowser：浏览器组的打分；还没检测时只给一项提示 */
func addBrowser(ctx context.Context, add func(cat, name string, w, score int, value, tip string), b *Browser, x probe.Exit) {
	const cat = "浏览器"
	if b == nil {
		add(cat, "浏览器侧检测", 6, 70, "还没检测", "点「浏览器检测」，在默认浏览器里检测 WebRTC、时区、语言等")
		return
	}
	// WebRTC：STUN 看到的公网地址必须就是出口 IP；本地候选不能暴露公网地址
	var leak []string
	for _, ip := range b.RTCSrflx {
		if x.IP != "" && ip != x.IP {
			leak = append(leak, ip)
		}
	}
	for _, ip := range b.RTCHost {
		if publicIP(ip) {
			leak = append(leak, ip)
		}
	}
	switch {
	case len(leak) > 0:
		g := probe.Geo{}
		if publicIP(leak[0]) {
			g = probe.Lookup(ctx, leak[0])
		}
		add(cat, "WebRTC 出口", 6, 0, strings.Join(leak, "、")+" "+g.Country, "WebRTC 暴露了非住宅出口的地址：在浏览器里禁用 WebRTC，或确认 UDP 也走代理")
	case len(b.RTCSrflx) == 0:
		add(cat, "WebRTC 出口", 6, 100, "没拿到公网候选，没有泄露", "")
	default:
		add(cat, "WebRTC 出口", 6, 100, b.RTCSrflx[0]+" = 出口", "")
	}
	// 浏览器时区
	switch {
	case b.TZ == "" || x.Timezone == "":
		add(cat, "浏览器时区", 3, 60, orUnknown(b.TZ), "")
	case b.TZ == x.Timezone:
		add(cat, "浏览器时区", 3, 100, b.TZ, "")
	case probe.SameOffset(b.TZ, x.Timezone):
		add(cat, "浏览器时区", 3, 80, b.TZ+"（与出口时差相同）", "")
	default:
		add(cat, "浏览器时区", 3, 0, b.TZ+" ≠ 出口 "+x.Timezone, "浏览器时区跟随系统，先把系统时区改成出口时区后重启浏览器")
	}
	// 浏览器语言
	first := ""
	if len(b.Langs) > 0 {
		first = b.Langs[0]
	}
	switch {
	case first == "" || x.Country == "":
		add(cat, "浏览器语言", 2, 60, orUnknown(strings.Join(b.Langs, ", ")), "")
	case strings.HasPrefix(strings.ToLower(first), "zh") && !zhRegions[strings.ToUpper(x.Country)]:
		add(cat, "浏览器语言", 2, 50, strings.Join(b.Langs, ", ")+"（出口在 "+x.Country+"）", "浏览器首选语言改成出口所在地区的语言，如 en-US")
	default:
		add(cat, "浏览器语言", 2, 100, strings.Join(b.Langs, ", "), "")
	}
	// Intl 区域设置与 HTTP 语言首标
	regionItem := func(name, tag, tip string) {
		r := regionOf(tag)
		switch {
		case tag == "":
			add(cat, name, 1, 60, "未知", "")
		case r == "" || x.Country == "" || strings.EqualFold(r, x.Country):
			add(cat, name, 1, 100, tag, "")
		default:
			add(cat, name, 1, 60, tag+"（出口在 "+x.Country+"）", tip)
		}
	}
	regionItem("Intl 区域设置", b.Locale, "系统设置 → 通用 → 语言与地区，把地区改成出口所在国家")
	al := b.AcceptLanguage
	if i := strings.IndexAny(al, ",;"); i >= 0 {
		al = al[:i]
	}
	regionItem("HTTP 语言首标", al, "浏览器首选语言改成出口所在地区的语言")
	// 渲染环境：软件渲染说明是虚拟机或无头浏览器
	gl := strings.ToLower(b.WebGL)
	switch {
	case gl == "":
		add(cat, "渲染环境", 2, 70, "拿不到 WebGL 渲染器", "")
	case strings.Contains(gl, "swiftshader") || strings.Contains(gl, "llvmpipe") || strings.Contains(gl, "vmware") || strings.Contains(gl, "virtualbox"):
		add(cat, "渲染环境", 2, 30, b.WebGL, "软件渲染容易被识别为虚拟机或自动化浏览器")
	default:
		add(cat, "渲染环境", 2, 100, b.WebGL, "")
	}
	// Client Hints：浏览器上报的平台与真实系统一致
	want := map[string]string{"darwin": "macos", "windows": "windows", "linux": "linux"}[runtime.GOOS]
	switch p := strings.ToLower(strings.Trim(b.UAPlatform, `"`)); {
	case p == "":
		add(cat, "Client Hints", 2, 80, "浏览器不支持（Safari、Firefox 属正常）", "")
	case want != "" && p != want:
		add(cat, "Client Hints", 2, 30, b.UAPlatform+"（本机是 "+runtime.GOOS+"）", "浏览器上报的平台与真实系统不一致，关掉修改平台的插件或设置")
	default:
		add(cat, "Client Hints", 2, 100, b.UAPlatform, "")
	}
}
