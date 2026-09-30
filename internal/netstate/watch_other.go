//go:build !darwin

package netstate

import "context"

/** Watch：其他系统没有路由变化通知，靠定时检查 */
func Watch(ctx context.Context, notify func()) error {
	<-ctx.Done()
	return nil
}
