/**
 * 网络状态：系统默认路由走哪块网卡、本机有没有公网 IPv6。TUN 生效时，IPv4 与 IPv6 的默认路由都应指向 Clash 的虚拟网卡。
 */
package netstate

import (
	"context"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

/** State：路由与地址 */
type State struct {
	// If4 / If6：访问公网 IPv4 / IPv6 时系统选用的网卡，空表示没有路由
	If4 string `json:"if4"`
	If6 string `json:"if6"`
	// GlobalIPv6：物理网卡上有公网 IPv6 地址（有的话 IPv6 也必须被 TUN 接管）
	GlobalIPv6 bool `json:"globalIPv6"`
}

var ifRe = regexp.MustCompile(`(?m)^\s*interface:\s*(\S+)`)

/** routeIf：查询访问某个地址时用的网卡 */
func routeIf(ctx context.Context, family, dst string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	args := []string{"-n", "get"}
	if family != "" {
		args = append(args, family)
	}
	out, err := exec.CommandContext(ctx, "/sbin/route", append(args, dst)...).Output()
	if err != nil {
		return ""
	}
	if m := ifRe.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	return ""
}

/** Read：读取当前状态 */
func Read(ctx context.Context) State {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return State{
		If4:        routeIf(ctx, "", "1.1.1.1"),
		If6:        routeIf(ctx, "-inet6", "2606:4700:4700::1111"),
		GlobalIPv6: hasGlobalIPv6(),
	}
}

/** hasGlobalIPv6：物理网卡上是否有公网 IPv6 地址（虚拟网卡与内网地址不算） */
func hasGlobalIPv6() bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || strings.HasPrefix(ifc.Name, "utun") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if ok && n.IP.To4() == nil && n.IP.IsGlobalUnicast() && !n.IP.IsPrivate() {
				return true
			}
		}
	}
	return false
}
