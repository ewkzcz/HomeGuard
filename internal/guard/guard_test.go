/**
 * 守护引擎测试：用假的 Clash 控制接口验证断开、锁定、暂停与恢复。
 */
package guard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"homeguard/internal/config"
	"homeguard/internal/mihomo"
	"homeguard/internal/netstate"
	"homeguard/internal/probe"
	"homeguard/internal/procs"
)

const res = "🏠 住宅出口 01"

/** fakeClash：可修改状态的假控制接口 */
type fakeClash struct {
	mu     sync.Mutex
	tun    bool
	now    string
	conns  []mihomo.Conn
	closed []string
	down   bool
}

func (f *fakeClash) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", 503)
		return
	}
	switch {
	case r.URL.Path == "/configs":
		json.NewEncoder(w).Encode(map[string]any{"mode": "rule", "ipv6": true, "sniffing": true, "find-process-mode": "always", "tun": map[string]any{"enable": f.tun, "device": "utun5"}})
	case r.URL.Path == "/version":
		json.NewEncoder(w).Encode(map[string]string{"version": "test"})
	case r.URL.Path == "/proxies":
		json.NewEncoder(w).Encode(map[string]any{"proxies": map[string]any{
			"住宅出口": map[string]any{"name": "住宅出口", "type": "Selector", "now": f.now, "all": []string{res, "基础节点"}},
			res:    map[string]any{"name": res, "type": "Socks5", "alive": true},
			"基础节点": map[string]any{"name": "基础节点", "type": "Selector", "now": "🇭🇰 香港 01", "all": []string{"🇭🇰 香港 01"}},
		}})
	case r.URL.Path == "/connections" && r.Method == "GET":
		json.NewEncoder(w).Encode(map[string]any{"connections": f.conns})
	case strings.HasPrefix(r.URL.Path, "/connections/") && r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, "/connections/")
		f.closed = append(f.closed, id)
		var keep []mihomo.Conn
		for _, c := range f.conns {
			if c.ID != id {
				keep = append(keep, c)
			}
		}
		f.conns = keep
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeClash) set(fn func()) { f.mu.Lock(); fn(); f.mu.Unlock() }

func (f *fakeClash) closedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

func conn(id, path, host, exit string) mihomo.Conn {
	return mihomo.Conn{ID: id, Chains: []string{exit, "住宅出口"}, Rule: "ProcessPathRegex", Start: time.Now(), Metadata: mihomo.Metadata{Host: host, ProcessPath: path, Process: filepath.Base(path)}}
}

/** env：引擎与假环境 */
type env struct {
	e       *Engine
	f       *fakeClash
	stopped map[int]bool
	if4     string
}

func setup(t *testing.T) *env {
	t.Helper()
	f := &fakeClash{tun: true, now: res}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	st, err := config.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(func(c *config.Config) { c.Controller = srv.URL }); err != nil {
		t.Fatal(err)
	}
	v := &env{f: f, stopped: map[int]bool{}, if4: "utun5"}
	e := New(st, dir)
	e.self = "/Applications/HomeGuard.app/Contents/MacOS/homeguard"
	e.ListProcs = func() ([]procs.Proc, error) {
		return []procs.Proc{{PID: 101, Path: "/Applications/Claude.app/Contents/MacOS/Claude"}, {PID: 102, Path: "/usr/bin/vim"}}, nil
	}
	e.Stop = func(pid int) error { v.stopped[pid] = true; return nil }
	e.Cont = func(pid int) error { delete(v.stopped, pid); return nil }
	e.ReadNet = func(context.Context) netstate.State { return netstate.State{If4: v.if4} }
	e.Probe = func(context.Context) probe.Exit { return probe.Exit{IP: "1.2.3.4", Country: "US"} }
	e.Views = func(context.Context) probe.Views { return probe.Views{} }
	e.snap.Exit = probe.Exit{IP: "1.2.3.4", Country: "US", City: "New York"}
	v.e = e
	return v
}

func (v *env) tick() { v.e.tick(context.Background(), true) }

func TestSafeTrafficIsLeftAlone(t *testing.T) {
	v := setup(t)
	v.f.set(func() {
		v.f.conns = []mihomo.Conn{
			conn("a", "/Applications/Claude.app/Contents/MacOS/Claude", "claude.ai", res),
			conn("b", "/usr/bin/curl", "example.com", "🇭🇰 香港 01"),
		}
	})
	v.tick()
	s := v.e.Snapshot()
	if !s.Safe || len(v.f.closedIDs()) != 0 || len(v.stopped) != 0 {
		t.Fatalf("安全时不应断开或暂停: safe=%v reason=%q closed=%v", s.Safe, s.Reason, v.f.closedIDs())
	}
	if len(s.Conns) != 1 || !s.Conns[0].Residential || s.Conns[0].App != "claude" {
		t.Fatalf("只应列出受保护的连接: %+v", s.Conns)
	}
}

func TestWrongExitClosesAndLatches(t *testing.T) {
	v := setup(t)
	v.f.set(func() {
		v.f.conns = []mihomo.Conn{
			conn("leak", "/Users/u/.local/share/claude/versions/2.1.0", "api.anthropic.com", "🇭🇰 香港 01"),
			conn("ok", "/Applications/Claude.app/Contents/MacOS/Claude", "claude.ai", res),
		}
	})
	v.tick()
	s := v.e.Snapshot()
	if s.Safe || s.Latch == "" {
		t.Fatalf("走错出口应锁定拦截: %+v", s)
	}
	closed := v.f.closedIDs()
	if len(closed) != 2 {
		t.Fatalf("拦截时应断开全部受保护的连接: %v", closed)
	}
	if !v.stopped[101] || v.stopped[102] {
		t.Fatalf("只暂停受保护的程序: %v", v.stopped)
	}
	// 问题还在时解除锁定，会立即重新拦截
	v.f.set(func() { v.f.conns = []mihomo.Conn{conn("leak2", "/x/bin/codex", "api.openai.com", "DIRECT")} })
	v.e.Clear()
	v.tick()
	if v.e.Snapshot().Safe {
		t.Fatal("问题还在，应重新拦截")
	}
	// 修好后解除锁定，恢复放行并继续程序
	v.e.Clear()
	v.tick()
	if s := v.e.Snapshot(); !s.Safe || len(v.stopped) != 0 {
		t.Fatalf("修好后应恢复: safe=%v reason=%q stopped=%v", s.Safe, s.Reason, v.stopped)
	}
}

func TestUnsafeConditions(t *testing.T) {
	cases := map[string]func(v *env){
		"住宅出口选了基础节点": func(v *env) { v.f.set(func() { v.f.now = "基础节点" }) },
		"TUN 关闭":     func(v *env) { v.f.set(func() { v.f.tun = false }) },
		"路由没经过 TUN":  func(v *env) { v.if4 = "en0" },
		"Clash 不可用":  func(v *env) { v.f.set(func() { v.f.down = true }) },
		"出口国家不支持":    func(v *env) { v.e.snap.Exit.Country = "HK" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			v := setup(t)
			v.f.set(func() {
				v.f.conns = []mihomo.Conn{conn("a", "/Applications/Claude.app/Contents/MacOS/Claude", "claude.ai", res)}
			})
			v.tick()
			if !v.e.Snapshot().Safe {
				t.Fatal("初始应安全")
			}
			breakIt(v)
			v.tick()
			s := v.e.Snapshot()
			if s.Safe || !v.stopped[101] {
				t.Fatalf("应拦截并暂停: safe=%v reason=%q stopped=%v", s.Safe, s.Reason, v.stopped)
			}
			if name != "Clash 不可用" && len(v.f.closedIDs()) != 1 {
				t.Fatalf("应断开受保护的连接: %v", v.f.closedIDs())
			}
		})
	}
}

func TestPrivateAndDisabled(t *testing.T) {
	v := setup(t)
	lan := conn("lan", "/Applications/Claude.app/Contents/MacOS/Claude", "", "DIRECT")
	lan.Metadata.DestinationIP = "192.168.1.10"
	v.f.set(func() { v.f.conns = []mihomo.Conn{lan} })
	v.tick()
	if !v.e.Snapshot().Safe || len(v.f.closedIDs()) != 0 {
		t.Fatal("访问局域网不算走错出口")
	}
	// 关闭守护：只显示，不断开不暂停
	if _, err := v.e.cfg.Update(func(c *config.Config) { c.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	v.f.set(func() { v.f.conns = []mihomo.Conn{conn("x", "/x/bin/codex", "api.openai.com", "DIRECT")} })
	v.tick()
	if len(v.f.closedIDs()) != 0 || len(v.stopped) != 0 {
		t.Fatal("关闭守护后不应断开或暂停")
	}
}
