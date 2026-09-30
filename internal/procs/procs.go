/**
 * 进程：列出本机进程及其程序路径，暂停（SIGSTOP）与继续（SIGCONT）。暂停后进程不再收发任何数据，继续后原样恢复。
 */
package procs

import "errors"

/** Proc：一个进程 */
type Proc struct {
	PID  int    `json:"pid"`
	Path string `json:"path"`
}

/** ErrUnsupported：当前系统不支持 */
var ErrUnsupported = errors.New("当前系统暂不支持暂停程序")
