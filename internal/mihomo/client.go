/**
 * Clash（mihomo 内核）控制接口：读取运行状态、分组、连接，断开连接。Clash Verge 默认通过本机套接字提供。
 */
package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

/** Client：控制接口客户端 */
type Client struct {
	base   string
	secret string
	hc     *http.Client
}

/**
 * New：创建客户端
 *
 * controller 取值：unix:/套接字路径（Clash Verge 默认）或 http://地址:端口
 */
func New(controller, secret string) *Client {
	c := &Client{secret: secret}
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, DisableCompression: true}
	if p, ok := strings.CutPrefix(controller, "unix:"); ok {
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", p)
		}
		c.base = "http://mihomo"
	} else {
		c.base = strings.TrimRight(controller, "/")
		// 控制接口在本机，不能走系统代理
		tr.Proxy = nil
	}
	c.hc = &http.Client{Transport: tr, Timeout: 2 * time.Second}
	return c
}

/** do：发送请求，out 非空时解析返回的 JSON */
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("Clash 返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

/** Tun：TUN 设置 */
type Tun struct {
	Enable bool   `json:"enable"`
	Device string `json:"device"`
}

/** Configs：运行中的核心设置 */
type Configs struct {
	Mode            string `json:"mode"`
	IPv6            bool   `json:"ipv6"`
	Tun             Tun    `json:"tun"`
	FindProcessMode string `json:"find-process-mode"`
	Sniffing        bool   `json:"sniffing"`
	MixedPort       int    `json:"mixed-port"`
}

/** Configs：读取核心设置 */
func (c *Client) Configs(ctx context.Context) (Configs, error) {
	var v Configs
	return v, c.do(ctx, "GET", "/configs", nil, &v)
}

/** Proxy：一个代理或分组 */
type Proxy struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Now         string   `json:"now"`
	All         []string `json:"all"`
	Alive       bool     `json:"alive"`
	DialerProxy string   `json:"dialer-proxy"`
}

/** IsGroup：是否为分组 */
func (p Proxy) IsGroup() bool {
	switch strings.ToLower(p.Type) {
	case "selector", "urltest", "fallback", "loadbalance", "relay":
		return true
	}
	return false
}

/**
 * IsResidentialType：住宅出口分组里的单个代理都算住宅代理，类型不限（socks5、http、vless 等）；
 * 分组（如退到「基础节点」）与直连、拒绝不算
 */
func (p Proxy) IsResidentialType() bool {
	if p.IsGroup() {
		return false
	}
	switch strings.ToLower(p.Type) {
	case "direct", "reject", "rejectdrop", "pass", "compatible", "":
		return false
	}
	return true
}

/** Proxies：全部代理与分组 */
func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) {
	var v struct {
		Proxies map[string]Proxy `json:"proxies"`
	}
	return v.Proxies, c.do(ctx, "GET", "/proxies", nil, &v)
}

/** Metadata：连接的来源与目标 */
type Metadata struct {
	Network       string `json:"network"`
	Type          string `json:"type"`
	SourceIP      string `json:"sourceIP"`
	DestinationIP string `json:"destinationIP"`
	Host          string `json:"host"`
	SniffHost     string `json:"sniffHost"`
	Process       string `json:"process"`
	ProcessPath   string `json:"processPath"`
}

/** Target：连接目标，优先域名 */
func (m Metadata) Target() string {
	if m.Host != "" {
		return m.Host
	}
	if m.SniffHost != "" {
		return m.SniffHost
	}
	return m.DestinationIP
}

/** Conn：一条连接；Chains[0] 是最终出口，其后是所在分组 */
type Conn struct {
	ID          string    `json:"id"`
	Upload      int64     `json:"upload"`
	Download    int64     `json:"download"`
	Start       time.Time `json:"start"`
	Chains      []string  `json:"chains"`
	Rule        string    `json:"rule"`
	RulePayload string    `json:"rulePayload"`
	Metadata    Metadata  `json:"metadata"`
}

/** Exit：最终出口 */
func (c Conn) Exit() string {
	if len(c.Chains) == 0 {
		return ""
	}
	return c.Chains[0]
}

/** Connections：当前全部连接 */
func (c *Client) Connections(ctx context.Context) ([]Conn, error) {
	var v struct {
		Connections []Conn `json:"connections"`
	}
	return v.Connections, c.do(ctx, "GET", "/connections", nil, &v)
}

/** Close：断开一条连接 */
func (c *Client) Close(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/connections/"+url.PathEscape(id), nil, nil)
}

/** Version：内核版本，用于测试连通 */
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	return v.Version, c.do(ctx, "GET", "/version", nil, &v)
}
