package fsutil

import (
	"os"
	"syscall"
	"unsafe"
)

// IsTerminal reports whether f is an interactive terminal. A character
// device is not enough: /dev/null is one too, and treating it as a terminal
// would send a TTY-less run (stdin redirected from /dev/null, as under
// cron, systemd or `ssh host cmd </dev/null`) down an interactive path.
// Only a device that answers the TCGETS terminal ioctl counts.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
