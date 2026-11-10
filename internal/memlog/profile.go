package memlog

import (
	"os"
	"runtime"
	"runtime/pprof"
)

// WriteHeapProfile menulis profil heap (format pprof gzip) ke path.
// runtime.GC() dipanggil lebih dulu supaya profil mencerminkan memori yang
// benar-benar masih hidup, bukan sampah yang belum dikoleksi. Ini hanya
// dipanggil atas permintaan owner (.memlog heap), tidak pernah oleh sampler.
func WriteHeapProfile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	runtime.GC()
	if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
		return err
	}
	return f.Sync()
}

// WriteGoroutineProfile menulis dump goroutine berisi stack lengkap (debug=2)
// ke path, untuk mencari goroutine yang menggantung.
func WriteGoroutineProfile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		return err
	}
	return f.Sync()
}
