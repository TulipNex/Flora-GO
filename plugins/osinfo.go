package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

func handleOSInfo(ctx context.Context, mctx *store.MessageContext) error {
	// Detail host tak boleh publik: gate eksplisit seperti .server dan .rss.
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}

	distro := linuxDistro()
	kernel := linuxKernelRelease()
	hostname, _ := os.Hostname()
	hostUptime, hostUptimeOK := linuxUptime()
	net := lookupIPInfo()

	msg := fmt.Sprintf(
		"🖥️ *OPERATING SYSTEM*\n"+
			"\n• Distro : %s\n"+
			"• Platform : %s\n"+
			"• Type : %s\n"+
			"• Release : %s\n"+
			"• Hostname : %s\n"+
			"• Uptime : %s\n"+
			"\n*Network*\n"+
			"• IP Publik : %s\n"+
			"• ISP : %s\n"+
			"• Lokasi : %s\n"+
			"• Zona Waktu : %s",
		dashStr(distro),
		runtime.GOOS,
		"Linux",
		dashStr(kernel),
		dashStr(hostname),
		durationOrDash(hostUptime, hostUptimeOK),
		dashStr(net.Query),
		dashStr(net.ISP),
		locationDisplay(net),
		dashStr(net.Timezone),
	)
	return store.ReplyText(ctx, mctx, msg)
}
