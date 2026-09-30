/**
 * 出口核验：经住宅出口访问两家 IP 情报接口，拿到出口 IP、国家、城市、时区、是否机房 IP。
 * 这些网址在分流脚本里固定走住宅出口，测到的就是 Claude 实际使用的出口。
 */
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

/** Exit：出口信息 */
type Exit struct {
	IP       string    `json:"ip"`
	Country  string    `json:"country"`
	Country2 string    `json:"country2"`
	Name     string    `json:"name"`
	Region   string    `json:"region"`
	City     string    `json:"city"`
	Org      string    `json:"org"`
	ASN      string    `json:"asn"`
	Timezone string    `json:"timezone"`
	Hosting  bool      `json:"hosting"`
	Proxy    bool      `json:"proxy"`
	At       time.Time `json:"at"`
	Err      string    `json:"err,omitempty"`
}

/** OK：拿到了出口 IP 与国家 */
func (e Exit) OK() bool { return e.IP != "" && e.Country != "" }

/** Client：探测用的 HTTP 客户端，不走系统代理，由 TUN 按规则转发 */
var Client = &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}

/** getJSON：请求并解析 JSON */
func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	resp, err := Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
}

/**
 * Run：核验一次出口
 *
 * 处理流程：
 * 1、同时请求 ip-api.com（主，含机房、代理标记）与 ipinfo.io（辅，交叉核对国家）
 * 2、两家都失败时返回错误；只有一家时用它的结果
 */
func Run(ctx context.Context) Exit {
	var (
		wg sync.WaitGroup
		a  struct {
			Status, CountryCode, Country, RegionName, City, Isp, Org, As, Timezone, Query string
			Hosting, Proxy                                                                bool
		}
		b  struct{ IP, Country, Region, City, Org, Timezone string }
		ea error
		eb error
	)
	// 1、并行请求
	wg.Add(2)
	go func() {
		defer wg.Done()
		ea = getJSON(ctx, "http://ip-api.com/json/?fields=status,country,countryCode,regionName,city,isp,org,as,timezone,query,hosting,proxy", &a)
		if ea == nil && a.Status != "success" {
			ea = fmt.Errorf("ip-api 返回 %s", a.Status)
		}
	}()
	go func() {
		defer wg.Done()
		eb = getJSON(ctx, "https://ipinfo.io/json", &b)
	}()
	wg.Wait()
	// 2、合并
	e := Exit{At: time.Now()}
	if ea == nil {
		e.IP, e.Country, e.Name, e.Region, e.City, e.Org, e.ASN, e.Timezone = a.Query, a.CountryCode, a.Country, a.RegionName, a.City, a.Isp, a.As, a.Timezone
		e.Hosting, e.Proxy = a.Hosting, a.Proxy
	}
	if eb == nil {
		e.Country2 = b.Country
		if e.IP == "" {
			e.IP, e.Country, e.Region, e.City, e.Org, e.Timezone = b.IP, b.Country, b.Region, b.City, b.Org, b.Timezone
		}
	}
	if ea != nil && eb != nil {
		e.Err = "两家 IP 情报接口都无法访问：" + ea.Error()
	}
	return e
}

/** Geo：一个 IP 的归属 */
type Geo struct {
	Country string `json:"countryCode"`
	Org     string `json:"isp"`
}

/** Lookup：查询任意 IP 的国家与运营商，失败返回空 */
func Lookup(ctx context.Context, ip string) Geo {
	var g struct {
		Status string `json:"status"`
		Geo
	}
	if getJSON(ctx, "http://ip-api.com/json/"+ip+"?fields=status,countryCode,isp", &g) != nil || g.Status != "success" {
		return Geo{}
	}
	return g.Geo
}

/** Status：访问一个网址，返回 HTTP 状态码，连不上返回 0 */
func Status(ctx context.Context, url string) int {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := Client.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
