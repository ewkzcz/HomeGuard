/**
 * 三视角出口与时区：沿用原项目的做法，分别访问国内网站、国外网站、需要翻墙才能到达的网站，看它们各自看到的出口 IP；
 * 再对比系统时区与出口 IP 所在时区。三个 IP 一致、时区一致，说明各条线路看到的是同一个出口。
 */
package probe

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

/** Views：一次对比结果 */
type Views struct {
	CN    string    `json:"cn"`
	Intl  string    `json:"intl"`
	GFW   string    `json:"gfw"`
	SysTZ string    `json:"sysTZ"`
	At    time.Time `json:"at"`
}

/** 各视角的回显接口，依次尝试，取第一个拿到的 IPv4 */
var (
	cnURLs   = []string{"http://members.3322.org/dyndns/getip", "https://whois.pconline.com.cn/ipJson.jsp?json=true", "https://qifu-api.baidubce.com/ip/local/geo/v1/district", "https://api.live.bilibili.com/xlive/web-room/v1/index/getIpInfo", "http://www.taobao.com/help/getip.php"}
	intlURLs = []string{"https://api.ipify.org", "https://icanhazip.com", "https://ipinfo.io/ip", "https://ifconfig.me/ip"}
	gfwURLs  = []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://api.ip.sb/ip", "https://api.myip.com"}
	ipv4Re   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
)

/** firstIP：依次访问，返回第一个有效的公网 IPv4 */
func firstIP(ctx context.Context, urls []string) string {
	for _, u := range urls {
		c, cancel := context.WithTimeout(ctx, 6*time.Second)
		req, _ := http.NewRequestWithContext(c, "GET", u, nil)
		req.Header.Set("User-Agent", "curl/8.0")
		resp, err := Client.Do(req)
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
			resp.Body.Close()
			for _, m := range ipv4Re.FindAllString(string(b), -1) {
				if ip := net.ParseIP(m); ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
					cancel()
					return m
				}
			}
		}
		cancel()
	}
	return ""
}

/** RunViews：三个视角并行测出口 IP，同时读取系统时区 */
func RunViews(ctx context.Context) Views {
	v := Views{SysTZ: SystemTZ(), At: time.Now()}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); v.CN = firstIP(ctx, cnURLs) }()
	go func() { defer wg.Done(); v.Intl = firstIP(ctx, intlURLs) }()
	go func() { defer wg.Done(); v.GFW = firstIP(ctx, gfwURLs) }()
	wg.Wait()
	return v
}

/** SystemTZ：系统时区（IANA 名称，如 America/New_York） */
func SystemTZ() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	p, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	if i := strings.Index(p, "zoneinfo/"); i >= 0 {
		return p[i+9:]
	}
	return ""
}

/** SameOffset：两个时区此刻的 UTC 偏移是否相同 */
func SameOffset(a, b string) bool {
	la, e1 := time.LoadLocation(a)
	lb, e2 := time.LoadLocation(b)
	if e1 != nil || e2 != nil {
		return false
	}
	now := time.Now()
	_, oa := now.In(la).Zone()
	_, ob := now.In(lb).Zone()
	return oa == ob
}
