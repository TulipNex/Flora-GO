// Package diag menampung alat diagnostik opsional untuk Flora GO.
//
// Leaf package: tidak mengimpor paket repo lain, sehingga bisa dipakai
// dari main.go tanpa risiko dependency cycle.
//
// Semua fitur di sini MATI secara default dan hanya aktif bila env terkait
// diisi. Tujuannya: profiling/observabilitas tidak boleh menambah beban
// atau membuka permukaan serangan saat bot berjalan normal di panel.
package diag

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "net/http/pprof"
)

// StartPprof menyalakan server pprof HANYA bila env PPROF_ADDR diisi.
// Contoh: PPROF_ADDR=127.0.0.1:6060
//
// Keamanan: host selain localhost/127.0.0.1/::1 dipaksa ke 127.0.0.1
// (dengan port yang diminta) agar endpoint profil tidak pernah terekspos
// ke jaringan publik secara tidak sengaja. Server berjalan di goroutine
// terpisah; kegagalan bind hanya dicatat, tidak mematikan bot.
func StartPprof() {
	raw := strings.TrimSpace(os.Getenv("PPROF_ADDR"))
	if raw == "" {
		return
	}

	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		// Izinkan bentuk "6060" (hanya port) juga.
		if n, perr := strconv.Atoi(raw); perr == nil && n > 0 {
			host, port = "127.0.0.1", raw
		} else {
			fmt.Printf("diag: PPROF_ADDR %q tidak valid (%v) — pprof tidak dinyalakan.\n", raw, err)
			return
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	low := strings.ToLower(host)
	if low != "localhost" && low != "127.0.0.1" && low != "::1" {
		fmt.Printf("diag: PPROF_ADDR host %q bukan localhost — dipaksa ke 127.0.0.1 (port %s).\n", host, port)
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, port)

	go func() {
		fmt.Printf("diag: pprof aktif di http://%s/debug/pprof/ (matikan dengan kosongkan PPROF_ADDR)\n", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			fmt.Printf("diag: server pprof berhenti: %v\n", err)
		}
	}()
}
