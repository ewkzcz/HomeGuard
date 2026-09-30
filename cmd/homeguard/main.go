/**
 * HomeGuard：Claude、Codex、ChatGPT 的住宅出口守护。
 *
 * 用法：
 *   homeguard          打开窗口（守护服务没运行时先在后台启动）
 *   homeguard serve    运行守护服务（关闭窗口后继续守护）
 *   homeguard quit     退出守护服务（会继续被暂停的程序）
 *   homeguard status   在终端查看当前状态
 */
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"homeguard/internal/config"
	"homeguard/internal/desktop"
	"homeguard/internal/guard"
	"homeguard/internal/server"
)

/** version：打包时写入 */
var version = "1.0.0"

func main() {
	// 从访达启动时系统可能带上 -psn_ 参数，忽略
	var args []string
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-psn_") {
			args = append(args, a)
		}
	}
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	dir, err := config.DataDir()
	if err != nil {
		log.Fatal(err)
	}
	switch cmd {
	case "serve":
		err = serve(dir)
	case "quit":
		err = call(dir, "POST", "/api/quit", nil)
	case "status":
		err = status(dir)
	case "":
		err = openWindow(dir)
	default:
		err = fmt.Errorf("未知命令：%s", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

/** loadKey：本机接口密钥，第一次运行时生成 */
func loadKey(dir string) (string, error) {
	p := filepath.Join(dir, "api.key")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	k := hex.EncodeToString(buf)
	return k, os.WriteFile(p, []byte(k), 0o600)
}

/**
 * serve：守护服务
 *
 * 处理流程：
 * 1、占用本机端口，已被占用说明守护已在运行
 * 2、启动守护引擎与本机接口
 * 3、收到退出信号或界面要求退出时，先继续被暂停的程序再退出
 */
func serve(dir string) error {
	cfg, err := config.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}
	key, err := loadKey(dir)
	if err != nil {
		return err
	}
	// 1、单实例
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Get().Port))
	if err != nil {
		return fmt.Errorf("守护已在运行，或端口 %d 被占用：%w", cfg.Get().Port, err)
	}
	// 2、引擎与接口
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	eng := guard.New(cfg, dir)
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	api := &server.Server{Cfg: cfg, Eng: eng, Key: key, Version: version, DataDir: dir, Quit: cancel}
	api.LoadBrowser()
	srv := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println(err)
			cancel()
		}
	}()
	// 3、退出
	<-ctx.Done()
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	<-done
	return nil
}

/** base：本机接口地址 */
func base(dir string) (string, string, error) {
	cfg, err := config.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		return "", "", err
	}
	key, err := loadKey(dir)
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.Get().Port), key, err
}

/** call：调用本机接口 */
func call(dir, method, path string, out any) error {
	b, key, err := base(dir)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(method, b+path, nil)
	req.Header.Set("X-Key", key)
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, msg)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

/** status：终端里查看状态 */
func status(dir string) error {
	var v struct {
		Snap guard.Snapshot `json:"snap"`
	}
	if err := call(dir, "GET", "/api/state", &v); err != nil {
		return fmt.Errorf("守护没有运行：%w", err)
	}
	s := v.Snap
	state := "安全"
	if !s.Enabled {
		state = "守护已关闭"
	} else if !s.Safe {
		state = "已拦截：" + s.Reason
	}
	fmt.Printf("状态：%s\n出口：%s %s %s\n住宅出口：%s\n", state, s.Exit.IP, s.Exit.Country, s.Exit.City, s.Current)
	for _, c := range s.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		fmt.Printf("  %s %s：%s\n", mark, c.Name, c.Detail)
	}
	return nil
}

/**
 * openWindow：打开窗口
 *
 * 处理流程：
 * 1、守护服务没运行时在后台启动，最多等 10 秒
 * 2、打开窗口；关闭窗口后守护继续在后台运行
 */
func openWindow(dir string) error {
	// 1、服务
	if call(dir, "GET", "/api/ping", nil) != nil {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		c := exec.Command(exe, "serve")
		detach(c)
		if err := c.Start(); err != nil {
			return err
		}
		_ = c.Process.Release()
		up := false
		for i := 0; i < 50 && !up; i++ {
			time.Sleep(200 * time.Millisecond)
			up = call(dir, "GET", "/api/ping", nil) == nil
		}
		if !up {
			return errors.New("守护服务没能启动，查看数据目录里的日志")
		}
	}
	// 2、窗口
	b, key, err := base(dir)
	if err != nil {
		return err
	}
	u := b + "/?k=" + key
	if err := desktop.Show(desktop.Window{Title: "HomeGuard", URL: u, Width: 1000, Height: 660}); err != nil {
		return desktop.OpenBrowser(u)
	}
	return nil
}
