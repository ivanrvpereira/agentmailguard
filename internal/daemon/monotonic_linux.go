package daemon

import (
	"strconv"
	"syscall"
	"unsafe"
)

// reloading is the systemd notification at the start of a reload. Type=notify-reload needs the monotonic clock.
func reloading() string {
	var ts syscall.Timespec
	const clockMonotonic = 1
	if _, _, e := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&ts)), 0); e != 0 {
		return "RELOADING=1"
	}
	return "RELOADING=1\nMONOTONIC_USEC=" + strconv.FormatInt(ts.Sec*1_000_000+ts.Nsec/1000, 10)
}
