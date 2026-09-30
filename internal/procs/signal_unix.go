//go:build !windows

package procs

import "syscall"

/** Stop：暂停进程 */
func Stop(pid int) error { return syscall.Kill(pid, syscall.SIGSTOP) }

/** Cont：继续进程；进程已退出时忽略 */
func Cont(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
