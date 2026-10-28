// Package netx menampung helper jaringan kecil yang dipakai lintas paket.
//
// Leaf package: NOL import dari repo sendiri, supaya bisa diimpor oleh
// plugins, internal/external, dan paket lain tanpa risiko dependency cycle.
package netx

import (
	"net"
	"net/http"
	"time"
)

// IsPublicIP true bila ip BUKAN alamat internal/khusus: privat (RFC1918),
// loopback, link-local (uni/multicast), unspecified, atau multicast. Dipakai
// guard anti-SSRF (mis. .get dan downloader) supaya URL tidak bisa diarahkan
// ke alamat internal server.
//
// Dipusatkan di sini — sebelumnya duplikat identik di plugins/ownerget.go
// dan internal/external/external.go.
func IsPublicIP(ip net.IP) bool {
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

// TunedTransport mengembalikan *http.Transport dengan connection pooling yang
// wajar (reuse koneksi, batas idle) supaya request berulang tidak membuka
// koneksi baru tiap kali. Dipakai lintas paket (external, ai, arena, ...).
func TunedTransport() *http.Transport {
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}
