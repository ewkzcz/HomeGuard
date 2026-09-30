//go:build windows

package procs

/** Stop：Windows 暂不支持 */
func Stop(pid int) error { return ErrUnsupported }

/** Cont：Windows 暂不支持 */
func Cont(pid int) error { return nil }
