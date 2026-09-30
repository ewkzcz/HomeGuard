/**
 * 守护引擎：实时核对受保护的流量是否全部从住宅出口出去，不满足就立即断开并暂停相关程序。
 *
 * 真正让流量只能走住宅的是 Clash 分流规则（零延迟）；这里负责盯住规则之外会出错的地方：
 * TUN 被关、路由没经过 TUN、住宅出口被切到别的节点、出口国家不对、有连接绕开了住宅出口。
 * 任何一项不满足：断开全部受保护的连接，暂停受保护的程序（它们不再收发任何数据），恢复后自动继续。
 */
package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"homeguard/internal/config"
	"homeguard/internal/mihomo"
	"homeguard/internal/netstate"
	"homeguard/internal/probe"
	"homeguard/internal/procs"
)

/** Check：一项守护条件；Critical 为真的项不满足时立即拦截 */
type Check struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Critical bool   `json:"critical"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
}

/** AppState：一个受保护程序的运行情况 */
type AppState struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Procs  []procs.Proc `json:"procs"`
	Paused []int        `json:"paused"`
	Conns  int          `json:"conns"`
	Up     int64        `json:"up"`
	Down   int64        `json:"down"`
}

/** ConnView：界面上显示的一条受保护连接 */
type ConnView struct {
	ID          string    `json:"id"`
	App         string    `json:"app"`
	AppName     string    `json:"appName"`
	Process     string    `json:"process"`
	Target      string    `json:"target"`
	Exit        string    `json:"exit"`
	Group       string    `json:"group"`
	Rule        string    `json:"rule"`
	Residential bool      `json:"residential"`
	Up          int64     `json:"up"`
	Down        int64     `json:"down"`
	Start       time.Time `json:"start"`
}

/** Event：一条记录 */
type Event struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // block 拦截、resume 恢复、close 断开、exit 出口、info、warn
	Text string    `json:"text"`
}

/** Snapshot：当前状态 */
type Snapshot struct {
	Enabled   bool       `json:"enabled"`
	PauseApps bool       `json:"pauseApps"`
	Safe      bool       `json:"safe"`
	Reason    string     `json:"reason"`
	Latch     string     `json:"latch"`
	Checks    []Check    `json:"checks"`
	Exit      probe.Exit `json:"exit"`
	// Views：三视角出口 IP 与系统时区，随出口核验一起刷新
	Views probe.Views `json:"views"`
	// TZMatch：系统时区与出口时区：same 相同、offset 时差相同、diff 不同、空为未知
	TZMatch     string   `json:"tzMatch"`
	ExitVia     string   `json:"exitVia"`
	Probing     bool     `json:"probing"`
	Group       string   `json:"group"`
	Current     string   `json:"current"`
	Residential []string `json:"residential"`
	Version     string   `json:"version"`
	// Controller：实际在用的 Clash 控制接口；ControllerSource：来自设置还是自动查找
	Controller       string `json:"controller"`
	ControllerSource string `json:"controllerSource"`
	// Script：分流脚本生效情况：active 生效、stale 脚本改过但 Clash 还没刷新、overridden 被订阅脚本覆盖或用的旧脚本、missing 没有使用
	Script string     `json:"script"`
	Apps   []AppState `json:"apps"`
	Conns  []ConnView `json:"conns"`
	Closed int        `json:"closed"`
	Blocks int        `json:"blocks"`
	At     time.Time  `json:"at"`
}

/** matcher：编译后的受保护程序 */
type matcher struct {
	id, name string
	res      []*regexp.Regexp
}

/** Engine：守护引擎 */
type Engine struct {
	cfg     *config.Store
	dataDir string
	self    string

	// 可替换的系统操作，便于测试
	ListProcs func() ([]procs.Proc, error)
	Stop      func(pid int) error
	Cont      func(pid int) error
	ReadNet   func(ctx context.Context) netstate.State
	Probe     func(ctx context.Context) probe.Exit
	Views     func(ctx context.Context) probe.Views

	wake     chan struct{}
	probeReq chan struct{}

	mu        sync.Mutex
	snap      Snapshot
	events    []Event
	seq       uint64
	paused    map[int]string
	logged    map[string]time.Time
	client    *mihomo.Client
	clientKey string
	matchers  []matcher
	matchKey  string
	// 设置里的控制接口连不上时，自动找到的接口
	found        *mihomo.Found
	discovering  bool
	lastDiscover time.Time

	// 只在引擎协程中读写
	cfgs      mihomo.Configs
	cfgsErr   error
	proxies   map[string]mihomo.Proxy
	ns        netstate.State
	running   []procs.Proc
	lastSlow  time.Time
	lastProcs time.Time
	lastNet   time.Time
	probeVia  string
	lastNow   string
	version   string
	wasSafe   bool
}

/** New：创建引擎 */
func New(cfg *config.Store, dataDir string) *Engine {
	self, _ := os.Executable()
	return &Engine{
		cfg: cfg, dataDir: dataDir, self: self,
		ListProcs: procs.List, Stop: procs.Stop, Cont: procs.Cont, ReadNet: netstate.Read, Probe: probe.Run, Views: probe.RunViews,
		wake: make(chan struct{}, 1), probeReq: make(chan struct{}, 1),
		paused: map[int]string{}, logged: map[string]time.Time{}, wasSafe: true,
	}
}

/** Wake：立即重新检查（路由变化、设置修改时调用） */
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

/** ProbeNow：立即核验出口 */
func (e *Engine) ProbeNow() {
	select {
	case e.probeReq <- struct{}{}:
	default:
	}
}

/**
 * Run：运行到上下文结束
 *
 * 处理流程：
 * 1、恢复上次异常退出时暂停的程序
 * 2、监听路由变化，变化时立即检查
 * 3、每 100 毫秒核对一次连接；Clash 设置与分组每 500 毫秒、进程每秒刷新一次
 * 4、退出时继续全部被暂停的程序
 */
func (e *Engine) Run(ctx context.Context) {
	// 1、上次遗留
	e.resumeLeftover()
	// 2、路由变化
	go func() { _ = netstate.Watch(ctx, e.Wake) }()
	go e.probeLoop(ctx)
	// 3、主循环
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// 4、退出
			e.resumeAll("守护已退出，已继续被暂停的程序")
			return
		case <-t.C:
			e.tick(ctx, false)
		case <-e.wake:
			e.tick(ctx, true)
		}
	}
}

/** probeLoop：按间隔核验出口；切换住宅代理或手动要求时立即核验 */
func (e *Engine) probeLoop(ctx context.Context) {
	next := time.After(2 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-next:
		case <-e.probeReq:
		}
		e.mu.Lock()
		e.snap.Probing = true
		e.probeVia = ""
		e.mu.Unlock()
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		var views probe.Views
		vdone := make(chan struct{})
		go func() { views = e.Views(pctx); close(vdone) }()
		x := e.Probe(pctx)
		<-vdone
		cancel()
		e.mu.Lock()
		e.snap.Views = views
		old := e.snap.Exit
		e.snap.Probing = false
		e.snap.ExitVia = e.probeVia
		if x.OK() || !old.OK() {
			e.snap.Exit = x
		} else {
			// 这次没测到，保留上次的结果，并记下错误
			e.snap.Exit.Err = x.Err
		}
		e.snap.TZMatch = tzMatch(views.SysTZ, e.snap.Exit.Timezone)
		e.mu.Unlock()
		if x.OK() {
			e.recordExit(x)
		}
		if x.OK() && x.IP != old.IP {
			e.event("exit", fmt.Sprintf("出口 %s · %s %s", x.IP, x.Country, x.City))
		} else if !x.OK() {
			e.event("warn", "出口核验失败："+x.Err)
		}
		e.Wake()
		next = time.After(time.Duration(e.cfg.Get().ProbeInterval) * time.Second)
	}
}

/** controller：实际使用的控制接口，设置里的连不上时用自动找到的 */
func (e *Engine) controller(c config.Config) (ctl, secret, source string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.found != nil {
		return e.found.Controller, e.found.Secret, "自动找到 · " + e.found.Source
	}
	return c.Controller, c.Secret, "设置"
}

/**
 * discover：后台查找控制接口，3 秒内只找一次，不阻塞检查循环
 *
 * 找到的和设置里的一样时不替换；都找不到时下次继续找
 */
func (e *Engine) discover(ctx context.Context, c config.Config) {
	e.mu.Lock()
	if e.discovering || time.Since(e.lastDiscover) < 3*time.Second {
		e.mu.Unlock()
		return
	}
	e.discovering, e.lastDiscover = true, time.Now()
	e.mu.Unlock()
	go func() {
		dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		f, ok := mihomo.Discover(dctx)
		cancel()
		e.mu.Lock()
		e.discovering = false
		changed := ok && f.Controller != c.Controller && (e.found == nil || e.found.Controller != f.Controller)
		if ok && f.Controller != c.Controller {
			e.found = &f
		}
		e.mu.Unlock()
		if changed {
			e.event("info", "自动找到 Clash 控制接口："+f.Controller+"（"+f.Source+"）")
			e.Wake()
		}
	}()
}

/** mihomoClient：控制接口变化时重建客户端 */
func (e *Engine) mihomoClient(c config.Config) *mihomo.Client {
	ctl, secret, _ := e.controller(c)
	c.Controller, c.Secret = ctl, secret
	key := c.Controller + "\x00" + c.Secret
	if e.client == nil || e.clientKey != key {
		e.client, e.clientKey = mihomo.New(c.Controller, c.Secret), key
	}
	return e.client
}

/** compile：受保护程序的路径规则，设置变化时重新编译；写错的规则跳过 */
func (e *Engine) compile(c config.Config) []matcher {
	b, _ := json.Marshal(c.Apps)
	if string(b) == e.matchKey {
		return e.matchers
	}
	var out []matcher
	for _, a := range c.Apps {
		m := matcher{id: a.ID, name: a.Name}
		for _, p := range a.Patterns {
			if re, err := regexp.Compile(p); err == nil {
				m.res = append(m.res, re)
			}
		}
		out = append(out, m)
	}
	e.matchers, e.matchKey = out, string(b)
	return out
}

/** appOf：进程路径属于哪个受保护程序，不属于返回 -1 */
func appOf(ms []matcher, path string) int {
	if path == "" {
		return -1
	}
	for i, m := range ms {
		for _, re := range m.res {
			if re.MatchString(path) {
				return i
			}
		}
	}
	return -1
}

/** domainOf：目标域名是否受保护 */
func domainOf(domains []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range domains {
		d = strings.ToLower(d)
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

var cgnat = func() *net.IPNet { _, n, _ := net.ParseCIDR("100.64.0.0/10"); return n }()

/**
 * privateDest：目标是本机、局域网或 Tailscale 组网内部，不上公网，不需要走住宅
 *
 * 带域名的连接按域名判断（fake-ip 模式下目标 IP 不可信），只有局域网与 Tailscale 的域名算内部
 */
func privateDest(m mihomo.Metadata) bool {
	if h := strings.ToLower(m.Target()); h != m.DestinationIP {
		return strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".ts.net") || h == "localhost"
	}
	ip := net.ParseIP(m.DestinationIP)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

/**
 * tick：一次检查
 *
 * 处理流程：
 * 1、刷新 Clash 设置、分组、路由与进程（按各自间隔，路由变化时全部刷新）
 * 2、读取连接，找出受保护的连接，发现没走住宅的立即断开并锁定拦截
 * 3、汇总守护条件，决定是否安全
 * 4、不安全：断开全部受保护的连接、暂停受保护的程序；恢复安全：继续被暂停的程序
 */
func (e *Engine) tick(ctx context.Context, full bool) {
	c := e.cfg.Get()
	cl := e.mihomoClient(c)
	ms := e.compile(c)
	now := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	// 1、慢速刷新
	if full || now.Sub(e.lastSlow) >= 500*time.Millisecond {
		e.lastSlow = now
		e.cfgs, e.cfgsErr = cl.Configs(ctx)
		if e.cfgsErr == nil {
			if p, err := cl.Proxies(ctx); err == nil {
				e.proxies = p
			}
			if e.version == "" {
				e.version, _ = cl.Version(ctx)
			}
		} else {
			e.proxies, e.version = nil, ""
			// 连不上：放弃之前自动找到的接口，重新查找
			e.mu.Lock()
			e.found = nil
			e.mu.Unlock()
			e.discover(context.Background(), c)
		}
	}
	if full || now.Sub(e.lastNet) >= time.Second {
		e.lastNet = now
		e.ns = e.ReadNet(ctx)
	}
	if full || now.Sub(e.lastProcs) >= time.Second {
		e.lastProcs = now
		if list, err := e.ListProcs(); err == nil {
			e.running = e.running[:0]
			for _, p := range list {
				if p.Path != e.self && appOf(ms, p.Path) >= 0 {
					e.running = append(e.running, p)
				}
			}
		}
	}
	clashOK := e.cfgsErr == nil

	// 住宅出口分组与其中的住宅代理
	group, hasGroup := e.proxies[c.ResidentialGroup]
	resSet := map[string]bool{}
	var resNames []string
	for _, n := range group.All {
		if p, ok := e.proxies[n]; ok && p.IsResidentialType() {
			resSet[n] = true
			resNames = append(resNames, n)
		}
	}
	if hasGroup && group.Now != e.lastNow {
		if e.lastNow != "" {
			e.event("info", "住宅出口切换为「"+group.Now+"」")
		}
		e.lastNow = group.Now
		e.ProbeNow()
	}

	// 2、连接
	var conns []mihomo.Conn
	if clashOK {
		conns, _ = cl.Connections(ctx)
	}
	e.mu.Lock()
	enabled := c.Enabled
	latch := e.snap.Latch
	e.mu.Unlock()
	apps := make([]AppState, len(ms))
	for i, m := range ms {
		apps[i] = AppState{ID: m.id, Name: m.name}
	}
	var views []ConnView
	var protected []mihomo.Conn
	closed := map[string]bool{}
	for _, cn := range conns {
		md := cn.Metadata
		// 核验请求实际走的出口
		if md.ProcessPath == e.self && (md.Target() == "ip-api.com" || md.Target() == "ipinfo.io") {
			e.mu.Lock()
			if e.snap.Probing {
				e.probeVia = cn.Exit()
			}
			e.mu.Unlock()
		}
		ai := appOf(ms, md.ProcessPath)
		if ai < 0 && !domainOf(c.Domains, md.Target()) {
			continue
		}
		if privateDest(md) {
			continue
		}
		v := ConnView{ID: cn.ID, Process: md.Process, Target: md.Target(), Exit: cn.Exit(), Rule: cn.Rule, Residential: resSet[cn.Exit()], Up: cn.Upload, Down: cn.Download, Start: cn.Start}
		if len(cn.Chains) > 1 {
			v.Group = cn.Chains[len(cn.Chains)-1]
		}
		if cn.RulePayload != "" {
			v.Rule += " · " + cn.RulePayload
		}
		if ai >= 0 {
			v.App, v.AppName = ms[ai].id, ms[ai].name
			apps[ai].Conns++
			apps[ai].Up += cn.Upload
			apps[ai].Down += cn.Download
		} else {
			v.AppName = md.Process
		}
		views = append(views, v)
		protected = append(protected, cn)
		// 走错出口：立即断开并锁定
		if enabled && !v.Residential {
			_ = cl.Close(ctx, cn.ID)
			closed[cn.ID] = true
			e.bump()
			who := v.AppName
			if who == "" {
				who = "未知程序"
			}
			msg := fmt.Sprintf("%s 访问 %s 走了「%s」，没走住宅出口", who, v.Target, orDirect(v.Exit))
			e.logOnce("v:"+who+v.Target, "close", "已断开："+msg)
			if latch == "" {
				latch = msg
				e.mu.Lock()
				e.snap.Latch = latch
				e.mu.Unlock()
			}
		}
	}

	// 3、守护条件
	checks := e.checks(c, clashOK, group, hasGroup, resSet, latch)
	reason := ""
	for _, ck := range checks {
		if ck.Critical && !ck.OK {
			reason = ck.Name + "：" + ck.Detail
			break
		}
	}
	safe := reason == ""

	// 4、执行
	if enabled && !safe {
		for _, cn := range protected {
			if closed[cn.ID] {
				continue
			}
			if err := cl.Close(ctx, cn.ID); err == nil {
				e.bump()
			}
		}
		if c.PauseApps {
			e.pauseRunning(ms)
		}
	}
	if (!enabled || safe || !c.PauseApps) && e.pausedCount() > 0 {
		e.resumeAll("线路恢复安全，已继续被暂停的程序")
	}
	if enabled && safe != e.wasSafe {
		if safe {
			e.event("resume", "线路安全，已放行")
		} else {
			e.event("block", "已拦截："+reason)
			e.mu.Lock()
			e.snap.Blocks++
			e.mu.Unlock()
		}
	}
	e.wasSafe = safe || !enabled

	// 进程与暂停情况（先取控制接口，下面持锁期间不能再调用它）
	ctl, _, ctlSrc := e.controller(c)
	e.mu.Lock()
	for _, p := range e.running {
		ai := appOf(ms, p.Path)
		apps[ai].Procs = append(apps[ai].Procs, p)
		if _, ok := e.paused[p.PID]; ok {
			apps[ai].Paused = append(apps[ai].Paused, p.PID)
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Start.After(views[j].Start) })
	if len(views) > 300 {
		views = views[:300]
	}
	s := &e.snap
	s.Enabled, s.PauseApps, s.Safe, s.Reason, s.Checks = enabled, c.PauseApps, safe, reason, checks
	s.Group, s.Current, s.Residential = c.ResidentialGroup, group.Now, resNames
	s.Apps, s.Conns, s.At, s.Version = apps, views, now, e.version
	s.Controller, s.ControllerSource = ctl, ctlSrc
	s.Script = scriptState(e.proxies, hasGroup, resNames)
	if s.Script != "active" && s.Script != "" && vergeStale() {
		s.Script = "stale"
	}
	e.mu.Unlock()
}

/** scriptState：分流脚本是否生效：有「基础节点」分组，且住宅代理经它连出 */
func scriptState(proxies map[string]mihomo.Proxy, hasGroup bool, res []string) string {
	if proxies == nil {
		return ""
	}
	base, ok := proxies["基础节点"]
	switch {
	case hasGroup && ok && base.IsGroup():
		for _, n := range res {
			if proxies[n].DialerProxy != "基础节点" {
				return "overridden"
			}
		}
		return "active"
	case hasGroup:
		return "overridden"
	default:
		return "missing"
	}
}

/** vergeStale：Clash Verge 正在运行的配置比全局扩展脚本旧，说明改了脚本还没刷新订阅 */
func vergeStale() bool {
	base, err := os.UserConfigDir()
	if err != nil {
		return false
	}
	dir := filepath.Join(base, "io.github.clash-verge-rev.clash-verge-rev")
	run, e1 := os.Stat(filepath.Join(dir, "clash-verge.yaml"))
	script, e2 := os.Stat(filepath.Join(dir, "profiles", "Script.js"))
	return e1 == nil && e2 == nil && run.ModTime().Before(script.ModTime())
}

/** tzMatch：系统时区与出口时区是否一致 */
func tzMatch(sys, exit string) string {
	switch {
	case sys == "" || exit == "":
		return ""
	case sys == exit:
		return "same"
	case probe.SameOffset(sys, exit):
		return "offset"
	default:
		return "diff"
	}
}

/** orDirect：没有出口名时视为直连 */
func orDirect(s string) string {
	if s == "" {
		return "DIRECT"
	}
	return s
}

/** checks：汇总守护条件 */
func (e *Engine) checks(c config.Config, clashOK bool, group mihomo.Proxy, hasGroup bool, resSet map[string]bool, latch string) []Check {
	var out []Check
	add := func(id, name string, ok, critical bool, detail, fix string) {
		out = append(out, Check{ID: id, Name: name, OK: ok, Critical: critical, Detail: detail, Fix: fix})
	}
	// Clash
	if !clashOK {
		detail := "连不上 Clash 控制接口，也没自动找到其他位置"
		if e.cfgsErr != nil {
			detail += "：" + e.cfgsErr.Error()
		}
		add("clash", "Clash 运行中", false, true, detail, "打开 Clash Verge；仍不行就在 设置 → Clash 里填写控制接口（Clash 设置里的「外部控制」地址）")
		add("tun", "TUN 模式", false, true, "无法确认", "")
	} else {
		add("clash", "Clash 运行中", true, true, "已连接", "")
		t := e.cfgs.Tun
		add("tun", "TUN 模式", t.Enable, true, map[bool]string{true: "已开启 · " + t.Device, false: "未开启，流量不经过 Clash"}[t.Enable], "在 Clash Verge 设置里打开 TUN 模式")
	}
	// 路由
	dev := e.cfgs.Tun.Device
	ok4 := clashOK && dev != "" && e.ns.If4 == dev
	add("route4", "IPv4 经过 TUN", ok4, true, routeDetail(e.ns.If4, dev), "TUN 没接管系统路由：重启 Clash Verge 的 TUN 模式")
	switch {
	case !e.ns.GlobalIPv6:
		add("route6", "IPv6 经过 TUN", true, true, "本机没有公网 IPv6", "")
	case e.ns.If6 == "" || (dev != "" && e.ns.If6 == dev):
		add("route6", "IPv6 经过 TUN", true, true, "已接管", "")
	default:
		add("route6", "IPv6 经过 TUN", false, true, "IPv6 走 "+e.ns.If6+"，绕过了 Clash，会暴露真实 IPv6", "在 Clash Verge 设置里打开 IPv6")
	}
	// 识别程序
	switch fpm := e.cfgs.FindProcessMode; {
	case !clashOK:
		add("process", "按程序识别", false, true, "无法确认", "")
	case fpm == "off":
		add("process", "按程序识别", false, true, "Clash 没有识别程序，无法按程序分流", "使用分流脚本（会开启按程序识别）")
	default:
		add("process", "按程序识别", true, true, "已开启", "")
	}
	// 住宅出口
	switch {
	case !clashOK:
		add("group", "住宅出口", false, true, "无法确认", "")
	case !hasGroup:
		add("group", "住宅出口", false, true, "Clash 里没有「"+c.ResidentialGroup+"」分组", "使用分流脚本，或在设置里填写住宅出口分组名")
	case !resSet[group.Now]:
		add("group", "住宅出口", false, true, "当前选的是「"+orDirect(group.Now)+"」，不是住宅代理", "在 Clash 的「"+c.ResidentialGroup+"」里选一个住宅代理")
	default:
		alive := e.proxies[group.Now].Alive
		d := group.Now
		if !alive {
			d += "（测速不通，请求会失败但不会泄露）"
		}
		add("group", "住宅出口", true, true, d, "")
	}
	add("sniff", "域名嗅探", clashOK && e.cfgs.Sniffing, false, map[bool]string{true: "已开启", false: "未开启，只有 IP 的连接认不出域名"}[e.cfgs.Sniffing], "使用分流脚本（会开启嗅探）")
	// 出口
	e.mu.Lock()
	x, via := e.snap.Exit, e.snap.ExitVia
	e.mu.Unlock()
	switch {
	case !x.OK():
		add("exit", "出口国家", true, false, "还没核验"+errSuffix(x.Err), "")
	case via != "" && !resSet[via]:
		add("exit", "出口国家", true, false, "核验请求走了「"+via+"」，结果不代表住宅出口", "使用分流脚本（核验网址固定走住宅出口）")
	case contains(c.Unsupported, x.Country):
		add("exit", "出口国家", false, true, x.Country+" 不在 Claude 服务范围", "换一个支持地区的住宅代理")
	case contains(c.Supported, x.Country):
		add("exit", "出口国家", true, true, x.Country+" "+x.City+" · "+x.IP, "")
	default:
		add("exit", "出口国家", true, false, x.Country+" 支持情况未知 · "+x.IP, "建议使用美国、日本、新加坡等地区")
	}
	// 走错出口的连接
	if latch != "" {
		add("latch", "连接都走住宅", false, true, latch, "检查分流规则后，点「恢复」重新放行")
	} else {
		add("latch", "连接都走住宅", true, true, "没有发现", "")
	}
	return out
}

func routeDetail(ifc, dev string) string {
	switch {
	case ifc == "":
		return "查不到默认路由"
	case ifc == dev:
		return "经过 " + ifc
	default:
		return "走 " + ifc + "，没经过 TUN"
	}
}

func errSuffix(s string) string {
	if s == "" {
		return ""
	}
	return "（" + s + "）"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

/** bump：断开连接计数 */
func (e *Engine) bump() {
	e.mu.Lock()
	e.snap.Closed++
	e.mu.Unlock()
}

/** pauseRunning：暂停正在运行的受保护程序 */
func (e *Engine) pauseRunning(ms []matcher) {
	changed := false
	for _, p := range e.running {
		e.mu.Lock()
		_, done := e.paused[p.PID]
		e.mu.Unlock()
		if done || p.PID == os.Getpid() {
			continue
		}
		if err := e.Stop(p.PID); err != nil {
			e.logOnce("stop:"+p.Path, "warn", fmt.Sprintf("无法暂停 %s：%v", filepath.Base(p.Path), err))
			continue
		}
		e.mu.Lock()
		e.paused[p.PID] = ms[appOf(ms, p.Path)].name
		e.mu.Unlock()
		changed = true
	}
	if changed {
		e.event("block", fmt.Sprintf("已暂停 %d 个受保护的进程", e.pausedCount()))
		e.savePaused()
	}
}

func (e *Engine) pausedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.paused)
}

/** resumeAll：继续全部被暂停的程序 */
func (e *Engine) resumeAll(msg string) {
	e.mu.Lock()
	pids := make([]int, 0, len(e.paused))
	for pid := range e.paused {
		pids = append(pids, pid)
	}
	e.paused = map[int]string{}
	e.mu.Unlock()
	if len(pids) == 0 {
		return
	}
	for _, pid := range pids {
		_ = e.Cont(pid)
	}
	e.savePaused()
	e.event("resume", msg)
}

/** pausedFile：记录被暂停的进程，异常退出后下次启动时继续它们 */
func (e *Engine) pausedFile() string { return filepath.Join(e.dataDir, "paused.json") }

func (e *Engine) savePaused() {
	if e.dataDir == "" {
		return
	}
	e.mu.Lock()
	b, _ := json.Marshal(e.paused)
	e.mu.Unlock()
	_ = os.WriteFile(e.pausedFile(), b, 0o600)
}

/** resumeLeftover：继续上次异常退出时没来得及继续的进程 */
func (e *Engine) resumeLeftover() {
	if e.dataDir == "" {
		return
	}
	b, err := os.ReadFile(e.pausedFile())
	if err != nil {
		return
	}
	var m map[int]string
	if json.Unmarshal(b, &m) == nil && len(m) > 0 {
		for pid := range m {
			_ = e.Cont(pid)
		}
		e.event("resume", fmt.Sprintf("已继续上次退出时暂停的 %d 个进程", len(m)))
	}
	_ = os.Remove(e.pausedFile())
}

/** event：记一条记录，同时写入日志文件 */
func (e *Engine) event(kind, text string) {
	e.mu.Lock()
	e.seq++
	ev := Event{Seq: e.seq, At: time.Now(), Kind: kind, Text: text}
	e.events = append(e.events, ev)
	if len(e.events) > 500 {
		e.events = e.events[len(e.events)-500:]
	}
	e.mu.Unlock()
	if e.dataDir != "" {
		if f, err := os.OpenFile(filepath.Join(e.dataDir, "events.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s [%s] %s\n", ev.At.Format("2006-01-02 15:04:05"), kind, text)
			f.Close()
		}
	}
}

/** logOnce：同一件事 10 秒内只记一次，避免刷屏 */
func (e *Engine) logOnce(key, kind, text string) {
	e.mu.Lock()
	last := e.logged[key]
	now := time.Now()
	if now.Sub(last) < 10*time.Second {
		e.mu.Unlock()
		return
	}
	e.logged[key] = now
	e.mu.Unlock()
	e.event(kind, text)
}

/** Snapshot：当前状态 */
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.snap
	return s
}

/** Events：序号大于 since 的记录 */
func (e *Engine) Events(since uint64) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Event
	for _, ev := range e.events {
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out
}

/** Clear：用户确认处理后解除「走错出口」的锁定，如果问题还在会立即重新拦截 */
func (e *Engine) Clear() {
	e.mu.Lock()
	had := e.snap.Latch != ""
	e.snap.Latch = ""
	e.mu.Unlock()
	if had {
		e.event("info", "已手动解除拦截，重新检查")
	}
	e.Wake()
}

/** CloseConn：手动断开一条连接 */
func (e *Engine) CloseConn(ctx context.Context, id string) error {
	return e.mihomoClient(e.cfg.Get()).Close(ctx, id)
}

/** TestClash：测试控制接口，返回内核版本 */
func TestClash(ctx context.Context, controller, secret string) (string, error) {
	return mihomo.New(controller, secret).Version(ctx)
}

/** ExitRecord：一次出口变化 */
type ExitRecord struct {
	IP      string    `json:"ip"`
	Country string    `json:"country"`
	At      time.Time `json:"at"`
}

/** exitsFile：出口变化历史，体检里用来判断出口是否稳定 */
func (e *Engine) exitsFile() string { return filepath.Join(e.dataDir, "exits.json") }

/** ExitHistory：最近 7 天的出口变化 */
func (e *Engine) ExitHistory() []ExitRecord {
	var list []ExitRecord
	if e.dataDir == "" {
		return list
	}
	if b, err := os.ReadFile(e.exitsFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

/** recordExit：出口 IP 变了才记一条，只保留 7 天 */
func (e *Engine) recordExit(x probe.Exit) {
	if e.dataDir == "" {
		return
	}
	list := e.ExitHistory()
	if n := len(list); n > 0 && list[n-1].IP == x.IP {
		return
	}
	list = append(list, ExitRecord{IP: x.IP, Country: x.Country, At: x.At})
	cut := time.Now().Add(-7 * 24 * time.Hour)
	for len(list) > 0 && list[0].At.Before(cut) {
		list = list[1:]
	}
	b, _ := json.Marshal(list)
	_ = os.WriteFile(e.exitsFile(), b, 0o600)
}
