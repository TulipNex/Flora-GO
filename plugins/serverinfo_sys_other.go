//go:build !linux

package plugins

// sysMemInfo stub non-Linux (pemanggil sudah gate isLinux() + fail-soft,
// tapi stub ini WAJIB ada supaya package tetap compile di laptop Windows).
func sysMemInfo() (total, free uint64) {
	return 0, 0
}

// kernelRelease stub non-Linux (lihat catatan di atas).
func kernelRelease() string {
	return ""
}
