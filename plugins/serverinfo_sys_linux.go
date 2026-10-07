//go:build linux

package plugins

import (
	"strings"
	"syscall"
)

// sysMemInfo membaca RAM via syscall.Sysinfo (Linux saja — file ini
// dikompilasi hanya dengan build tag linux).
func sysMemInfo() (total, free uint64) {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err != nil {
		return 0, 0
	}
	unit := uint64(si.Unit)
	if unit == 0 {
		unit = 1
	}
	return si.Totalram * unit, si.Freeram * unit
}

// kernelRelease membaca versi kernel via uname (Linux saja).
func kernelRelease() string {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range uts.Release {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}
