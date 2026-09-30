//go:build darwin

package netstate

import (
	"context"
	"syscall"
	"time"
)

/**
 * Watch：路由或网卡一有变化就回调（关闭 TUN、切换网络时立即触发），同一批变化 30 毫秒内只回调一次
 */
func Watch(ctx context.Context, notify func()) error {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		syscall.Close(fd)
	}()
	ch := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				time.Sleep(30 * time.Millisecond)
				select {
				case <-ch:
				default:
				}
				notify()
			}
		}
	}()
	buf := make([]byte, 4096)
	for {
		if _, err := syscall.Read(fd, buf); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
