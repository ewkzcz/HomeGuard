//go:build darwin

package procs

/*
#include <libproc.h>
#include <sys/proc_info.h>
*/
import "C"

import (
	"errors"
	"unsafe"
)

/** List：全部进程及其程序路径（取不到路径的进程跳过） */
func List() ([]Proc, error) {
	n := C.proc_listallpids(nil, 0)
	if n <= 0 {
		return nil, errors.New("无法列出进程")
	}
	// 多留余量，两次调用之间可能有新进程
	buf := make([]C.int, int(n)+128)
	n = C.proc_listallpids(unsafe.Pointer(&buf[0]), C.int(len(buf))*C.int(unsafe.Sizeof(buf[0])))
	if n <= 0 {
		return nil, errors.New("无法列出进程")
	}
	var path [C.PROC_PIDPATHINFO_MAXSIZE]C.char
	out := make([]Proc, 0, int(n))
	for _, pid := range buf[:int(n)] {
		if pid <= 0 {
			continue
		}
		r := C.proc_pidpath(pid, unsafe.Pointer(&path[0]), C.uint32_t(len(path)))
		if r <= 0 {
			continue
		}
		out = append(out, Proc{PID: int(pid), Path: C.GoStringN(&path[0], r)})
	}
	return out, nil
}
