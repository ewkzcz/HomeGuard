/**
 * 本机接口：桌面窗口通过它读取守护状态、修改设置。只监听 127.0.0.1，并要求密钥，其他本机程序无法关闭守护。
 */
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homeguard/internal/checkup"
	"homeguard/internal/clashscript"
	"homeguard/internal/config"
	"homeguard/internal/desktop"
	"homeguard/internal/guard"
	"homeguard/internal/mihomo"
)

//go:embed web
var webFS embed.FS

/** cookieName：窗口首次打开时用网址里的密钥换成 Cookie */
const cookieName = "cc_k"

/** Server：本机接口 */
type Server struct {
	Cfg     *config.Store
	Eng     *guard.Engine
	Key     string
	Version string
	DataDir string
	Quit    func()

	mu      sync.Mutex
	report  *checkup.Report
	running bool
	// 浏览器检测：一次性口令与最近一次结果
	browserToken string
	browserExp   time.Time
	browser      *checkup.Browser
}

/** Handler：全部路由 */
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(web))
	mux.HandleFunc("GET /{$}", s.index(files))
	for _, f := range []string{"/app.css", "/app.js", "/icons.js", "/icon.svg", "/browser.js"} {
		mux.Handle("GET "+f, files)
	}
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"version": s.Version}) })
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("POST /api/guard", s.setGuard)
	mux.HandleFunc("POST /api/clear", func(w http.ResponseWriter, r *http.Request) { s.Eng.Clear(); writeJSON(w, ok) })
	mux.HandleFunc("POST /api/probe", func(w http.ResponseWriter, r *http.Request) { s.Eng.ProbeNow(); writeJSON(w, ok) })
	mux.HandleFunc("POST /api/conn/close", s.closeConn)
	mux.HandleFunc("GET /api/checkup", s.getCheckup)
	mux.HandleFunc("POST /api/checkup", s.runCheckup)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.Cfg.Get()) })
	mux.HandleFunc("POST /api/config", s.setConfig)
	mux.HandleFunc("POST /api/browser/start", s.browserStart)
	mux.HandleFunc("GET /browser", s.browserPage(files))
	mux.HandleFunc("POST /browser/report", s.browserReport)
	mux.HandleFunc("POST /api/clash/test", s.testClash)
	mux.HandleFunc("GET /api/clash/diagnose", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, mihomo.Diagnose(r.Context()))
	})
	mux.HandleFunc("GET /api/clash-script", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"script": clashscript.Template})
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ok)
		go func() { time.Sleep(200 * time.Millisecond); s.Quit() }()
	})
	return s.guardHTTP(mux)
}

var ok = map[string]bool{"ok": true}

/**
 * guardHTTP：访问限制
 *
 * 处理流程：
 * 1、只接受以本机地址访问（防止网页借 DNS 重绑定冒充本机）
 * 2、修改类请求的来源必须是本窗口
 * 3、接口需要密钥：Cookie 或请求头 X-Key
 */
func (s *Server) guardHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1、主机名
		host := r.Host
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", 403)
			return
		}
		// 2、来源
		if o := r.Header.Get("Origin"); o != "" && r.Method != "GET" && o != "http://"+r.Host {
			http.Error(w, "forbidden", 403)
			return
		}
		// 3、密钥（页面本身与静态文件由 index 单独处理）
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.authed(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

/** authed：请求是否带了正确的密钥 */
func (s *Server) authed(r *http.Request) bool {
	k := r.Header.Get("X-Key")
	if c, err := r.Cookie(cookieName); err == nil && k == "" {
		k = c.Value
	}
	return k != "" && subtle.ConstantTimeCompare([]byte(k), []byte(s.Key)) == 1
}

/** index：首页；网址带密钥时写入 Cookie 并去掉网址里的密钥 */
func (s *Server) index(files http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k := r.URL.Query().Get("k"); k != "" {
			if subtle.ConstantTimeCompare([]byte(k), []byte(s.Key)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: k, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if !s.authed(r) {
			http.Error(w, "请从 HomeGuard 应用打开", 401)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		files.ServeHTTP(w, r)
	}
}

/** payload：推给界面的状态 */
func (s *Server) payload(since uint64) map[string]any {
	s.mu.Lock()
	running, browserAt := s.running, time.Time{}
	if s.browser != nil {
		browserAt = s.browser.At
	}
	s.mu.Unlock()
	return map[string]any{"snap": s.Eng.Snapshot(), "events": s.Eng.Events(since), "checkupRunning": running, "browserAt": browserAt}
}

/** state：一次性读取全部状态 */
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	p := s.payload(0)
	p["config"] = s.Cfg.Get()
	p["version"] = s.Version
	p["dataDir"] = s.DataDir
	s.mu.Lock()
	p["report"] = s.report
	p["browser"] = s.browser
	s.mu.Unlock()
	writeJSON(w, p)
}

/** stream：每 400 毫秒推送一次状态与新记录（Server-Sent Events） */
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		p := s.payload(since)
		if evs, _ := p["events"].([]guard.Event); len(evs) > 0 {
			since = evs[len(evs)-1].Seq
		}
		b, _ := json.Marshal(p)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return
		}
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

/** setGuard：守护开关与暂停程序开关 */
func (s *Server) setGuard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled   *bool `json:"enabled"`
		PauseApps *bool `json:"pauseApps"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Cfg.Update(func(c *config.Config) {
		if in.Enabled != nil {
			c.Enabled = *in.Enabled
		}
		if in.PauseApps != nil {
			c.PauseApps = *in.PauseApps
		}
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Eng.Wake()
	writeJSON(w, c)
}

/** closeConn：手动断开一条连接 */
func (s *Server) closeConn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.Eng.CloseConn(r.Context(), in.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, ok)
}

/** getCheckup：上次体检结果 */
func (s *Server) getCheckup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{"report": s.report, "running": s.running})
}

/** runCheckup：后台体检一次，结果通过状态推送与 GET /api/checkup 获取 */
func (s *Server) runCheckup(w http.ResponseWriter, r *http.Request) {
	s.startCheckup()
	writeJSON(w, map[string]bool{"running": true})
}

/** startCheckup：已在体检时不重复开始 */
func (s *Server) startCheckup() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	browser := s.browser
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		rep := checkup.Run(ctx, s.Eng.Snapshot(), s.Cfg.Get(), s.Eng.ExitHistory(), browser)
		s.mu.Lock()
		s.report, s.running = &rep, false
		s.mu.Unlock()
	}()
}

/** setConfig：修改设置，只改传入的项 */
func (s *Server) setConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Controller       *string      `json:"controller"`
		Secret           *string      `json:"secret"`
		ResidentialGroup *string      `json:"residentialGroup"`
		ProbeInterval    *int         `json:"probeInterval"`
		Domains          []string     `json:"domains"`
		Apps             []config.App `json:"apps"`
		Theme            *string      `json:"theme"`
		ResetApps        bool         `json:"resetApps"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Cfg.Update(func(c *config.Config) {
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = strings.TrimSpace(*v)
			}
		}
		set(&c.Controller, in.Controller)
		set(&c.Secret, in.Secret)
		set(&c.ResidentialGroup, in.ResidentialGroup)
		set(&c.Theme, in.Theme)
		if in.ProbeInterval != nil {
			c.ProbeInterval = *in.ProbeInterval
		}
		if in.Domains != nil {
			c.Domains = clean(in.Domains)
		}
		if in.Apps != nil {
			c.Apps = in.Apps
		}
		if in.ResetApps {
			c.Apps = config.DefaultApps()
		}
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Eng.Wake()
	writeJSON(w, c)
}

/** clean：去掉空行与首尾空格 */
func clean(list []string) []string {
	var out []string
	for _, x := range list {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

/** testClash：测试控制接口 */
func (s *Server) testClash(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Controller string `json:"controller"`
		Secret     string `json:"secret"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v, err := guard.TestClash(r.Context(), in.Controller, in.Secret)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"version": v})
}

/** browserFile：最近一次浏览器检测结果，重启后仍可用于体检 */
func (s *Server) browserFile() string { return filepath.Join(s.DataDir, "browser.json") }

/** LoadBrowser：启动时读取上次的浏览器检测结果 */
func (s *Server) LoadBrowser() {
	var b checkup.Browser
	if data, err := os.ReadFile(s.browserFile()); err == nil && json.Unmarshal(data, &b) == nil {
		s.mu.Lock()
		s.browser = &b
		s.mu.Unlock()
	}
}

/**
 * browserStart：开始浏览器检测
 *
 * 生成 10 分钟内有效的一次性口令，用默认浏览器打开检测页（默认浏览器没有窗口的登录信息，靠口令放行）
 */
func (s *Server) browserStart(w http.ResponseWriter, r *http.Request) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		writeErr(w, err)
		return
	}
	s.mu.Lock()
	s.browserToken, s.browserExp = hex.EncodeToString(buf), time.Now().Add(10*time.Minute)
	token := s.browserToken
	s.mu.Unlock()
	u := "http://" + r.Host + "/browser?t=" + token
	if err := desktop.OpenBrowser(u); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"url": u})
}

/** tokenOK：口令有效 */
func (s *Server) tokenOK(t string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t != "" && s.browserToken != "" && time.Now().Before(s.browserExp) && subtle.ConstantTimeCompare([]byte(t), []byte(s.browserToken)) == 1
}

/** browserPage：检测页 */
func (s *Server) browserPage(files http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(r.URL.Query().Get("t")) {
			http.Error(w, "检测链接已失效，请回到 HomeGuard 重新点「浏览器检测」", 403)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'")
		w.Header().Set("Cache-Control", "no-store")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/browser.html"
		files.ServeHTTP(w, r2)
	}
}

/** browserReport：收下检测结果（口令只能用一次），保存后重新体检 */
func (s *Server) browserReport(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r.URL.Query().Get("t")) {
		http.Error(w, `{"error":"检测链接已失效"}`, 403)
		return
	}
	var b checkup.Browser
	if !readJSON(w, r, &b) {
		return
	}
	b.AcceptLanguage, b.UserAgent, b.At = r.Header.Get("Accept-Language"), r.UserAgent(), time.Now()
	if b.UAPlatform == "" {
		b.UAPlatform = strings.Trim(r.Header.Get("Sec-CH-UA-Platform"), `"`)
	}
	s.mu.Lock()
	s.browser, s.browserToken = &b, ""
	s.mu.Unlock()
	if data, err := json.Marshal(b); err == nil {
		_ = os.WriteFile(s.browserFile(), data, 0o600)
	}
	s.startCheckup()
	writeJSON(w, ok)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		http.Error(w, `{"error":"请求格式不对"}`, 400)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(500)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
