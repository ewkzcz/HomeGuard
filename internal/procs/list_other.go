//go:build !darwin

package procs

/** List：其他系统暂不支持列出进程 */
func List() ([]Proc, error) { return nil, ErrUnsupported }
