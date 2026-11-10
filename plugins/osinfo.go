package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterOSInfo)
}

// RegisterOSInfo mendaftarkan command `.os` KHUSUS owner: info sistem operasi
// + jaringan server. Dipisah dari `.server` supaya tidak ada baris yang
// tampil dobel — pembaca /proc dan /etc dipakai ulang dari serverinfo.go.
func RegisterOSInfo(r *Registry) {
	r.Register(Command{
		Name:        "os",
		Description: "Info OS & jaringan server",
		Category:    CategoryOwner,
		Handler:     handleOSInfo,
	})
}

// ipAPIResponse adalah subset respons ip-api.com yang kita pakai (gratis,
// tanpa key). Field lain diabaikan.
type ipAPIResponse struct {
	Status   string `json:"status"`
	Country  string `json:"country"`
	City     string `json:"city"`
	ISP      string `json:"isp"`
	Query    string `json:"query"`
	Timezone string `json:"timezone"`
}

// ipInfoCache menyimpan hasil lookup selama proses hidup: lokasi server
// tidak berubah-ubah, jadi 1 request per start cukup (tidak tiap command).
var ipInfoCache struct {
	sync.Once
	data ipAPIResponse
}

// lookupIPInfo menanyakan geolokasi IP publik ke ip-api.com (timeout 10
// detik). Gagal (panel offline/API mati) = struct kosong, dan pemanggil
// menampilkan "-" per baris — command tidak pernah gagal total karenanya.
func lookupIPInfo() ipAPIResponse {
	ipInfoCache.Once.Do(func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get("http://ip-api.com/json/?fields=status,country,city,isp,query,timezone")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		var out ipAPIResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return
		}
		if strings.TrimSpace(out.Status) != "success" {
			return
		}
		ipInfoCache.data = out
	})
	return ipInfoCache.data
}

// locationDisplay merangkai "kota, negara", salah satunya bila sebagian
// hilang, "-" bila keduanya kosong.
func locationDisplay(net ipAPIResponse) string {
	city := strings.TrimSpace(net.City)
	country := strings.TrimSpace(net.Country)
	switch {
	case city != "" && country != "":
		return city + ", " + country
	case city != "":
		return city
	case country != "":
		return country
	default:
		return "-"
	}
}

// shortISP mengambil dua kata pertama dari nama ISP (mis. "Oliver Horscht"
// dari 'Oliver Horscht is trading as "SYNLINQ"'). Gagal parse = string asli.
func shortISP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Buang bagian setelah tanda kutip/koma/semicolon.
	for _, sep := range []string{`"`, "'", ",", ";"} {
		if i := strings.Index(raw, sep); i >= 0 {
			raw = strings.TrimSpace(raw[:i])
		}
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return raw
	}
	if len(fields) == 1 {
		return fields[0]
	}
	return fields[0] + " " + fields[1]
}

func handleOSInfo(ctx context.Context, mctx *store.MessageContext) error {
	// Detail host tak boleh publik: gate eksplisit seperti .server dan .memory.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	distro := linuxDistro()
	kernel := linuxKernelRelease()
	hostUptime, hostUptimeOK := linuxUptime()
	net := lookupIPInfo()
	isp := shortISP(net.ISP)

	msg := fmt.Sprintf(
		"*OPERATING SYSTEM*\n\n"+
			"```%-11s: %s\n"+
			"%-11s: %s\n"+
			"%-11s: %s\n"+
			"%-11s: %s\n"+
			"%-11s: %s\n"+
			"\nNetwork\n"+
			"%-11s: %s\n"+
			"%-11s: %s\n"+
			"%-11s: %s\n"+
			"%-11s: %s```",
		"Distro", dashStr(distro),
		"Platform", runtime.GOOS,
		"Type", "Linux",
		"Release", dashStr(kernel),
		"Uptime", durationOrDash(hostUptime, hostUptimeOK),
		"IP Publik", dashStr(net.Query),
		"ISP", dashStr(isp),
		"Lokasi", locationDisplay(net),
		"Zona Waktu", dashStr(net.Timezone),
	)
	return store.ReplyText(ctx, mctx, msg)
}
