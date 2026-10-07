package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"wabot-go/internal/messaging"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterSpeedtest)
}

// speedtestTimeout membatasi satu pengujian penuh (download+upload di link
// lambat bisa >60 detik). Flag license di bawah WAJIB: run pertama tanpa
// --accept-license/--accept-gdpr berhenti menunggu prompt interaktif.
const speedtestTimeout = 3 * time.Minute

func RegisterSpeedtest(r *Registry) {
	r.Register(Command{
		Name:        "speedtest",
		Aliases:     []string{"st"},
		Description: "Tes kecepatan internet panel (kartu hasil Ookla)",
		Category:    CategoryOwner,
		Handler:     handleSpeedtest,
	})
}

// handleSpeedtest menjalankan speedtest CLI di goroutine (pola downloader):
// pengujiannya sendiri makan puluhan detik sampai menit, event loop tidak
// boleh diblokir. Hasil dikirim sebagai SATU linkcard (grafik + teks).
func handleSpeedtest(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if _, err := sendDlStatus(ctx, mctx, "Mengetes kecepatan internet..."); err != nil {
		return err
	}
	go func() {
		bg := context.Background()
		if rerr := runSpeedtest(bg, mctx); rerr != nil {
			_ = store.ReplyText(bg, mctx, rerr.Error())
		}
	}()
	return nil
}

// speedtestLatency adalah rincian latensi (ms). Pointer di parent
// menandakan blok ini opsional: nil = server tidak mengirimnya.
type speedtestLatency struct {
	IQM    float64 `json:"iqm"`
	Low    float64 `json:"low"`
	High   float64 `json:"high"`
	Jitter float64 `json:"jitter"`
}

// speedtestIO adalah satu arah transfer (download/upload). Bandwidth dalam
// byte/detik, Bytes total byte terpakai.
type speedtestIO struct {
	Bandwidth float64           `json:"bandwidth"`
	Bytes     float64           `json:"bytes"`
	Elapsed   float64           `json:"elapsed"`
	Latency   *speedtestLatency `json:"latency"`
}

// speedtestResult adalah subset JSON speedtest --format=json yang kita
// pakai. Section berupa pointer supaya bagian yang hilang (versi CLI beda)
// terdeteksi sebagai "-" bukan angka 0 yang menyesatkan.
type speedtestResult struct {
	Ping *struct {
		Latency float64 `json:"latency"`
		Jitter  float64 `json:"jitter"`
		Low     float64 `json:"low"`
		High    float64 `json:"high"`
	} `json:"ping"`
	Download   *speedtestIO `json:"download"`
	Upload     *speedtestIO `json:"upload"`
	PacketLoss *float64     `json:"packetLoss"`
	ISP        string       `json:"isp"`
	Server     *struct {
		ID       interface{} `json:"id"`
		Name     string      `json:"name"`
		Location string      `json:"location"`
		Country  string      `json:"country"`
	} `json:"server"`
	Result *struct {
		URL string `json:"url"`
	} `json:"result"`
}

// runSpeedtest mengeksekusi CLI, parse JSON, lalu mengirim linkcard hasil.
func runSpeedtest(ctx context.Context, mctx *store.MessageContext) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, speedtestTimeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "speedtest",
		"--format=json", "--accept-license", "--accept-gdpr",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("tes melebihi %s, dibatalkan.", speedtestTimeout)
		}
		out := append(stdout.Bytes(), stderr.Bytes()...)
		if len(out) == 0 {
			return fmt.Errorf("speedtest gagal (binary tidak ada?): %v", err)
		}
		return fmt.Errorf("speedtest gagal: %v", firstDlErrorLine(out))
	}
	if stdout.Len() > 1<<20 {
		return fmt.Errorf("output speedtest terlalu besar")
	}

	var res speedtestResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return fmt.Errorf("output bukan JSON valid")
	}

	pageURL := ""
	if res.Result != nil {
		pageURL = strings.TrimSpace(res.Result.URL)
	}
	thumbURL := ""
	if pageURL != "" {
		thumbURL = pageURL + ".png"
	}
	card := messaging.LinkCard{
		PageURL:     pageURL,
		Title:       "Speedtest Result",
		Description: "Powered By Ookla",
		ThumbURL:    thumbURL,
		Body:        formatSpeedtestBody(&res),
	}
	// PageURL kosong (versi CLI tak keluarkan result.url) tetap terkirim
	// sebagai kartu teks + thumbnail bila ada — SendLinkCard toleran.
	if card.PageURL == "" {
		card.PageURL = "https://www.speedtest.net"
	}
	if err := messaging.SendLinkCard(ctx, mctx, card); err != nil {
		return fmt.Errorf("gagal kirim kartu: %v", err)
	}
	return nil
}

// formatSpeedtestBody merangkai blok statistik referensi owner dalam blok
// kode monospace WhatsApp (tepat 3 backtick di baris awal dan akhir —
// alignment kolom spasi hanya rapi di font fixed-width). Field yang hilang
// dari JSON ditampilkan "-" per baris (bukan 0 yang menyesatkan).
func formatSpeedtestBody(res *speedtestResult) string {
	ms := func(v float64, ok bool) string {
		if !ok {
			return "-"
		}
		return fmt.Sprintf("%.2f ms", v)
	}
	var sb strings.Builder
	sb.WriteString("```\n")

	server, serverOK := "-", false
	city, cityOK := "-", false
	sid, sidOK := "-", false
	if res.Server != nil {
		if strings.TrimSpace(res.Server.Name) != "" {
			server, serverOK = res.Server.Name, true
		}
		if strings.TrimSpace(res.Server.Location) != "" {
			city, cityOK = res.Server.Location, true
		}
		if sidStr := strings.TrimSpace(fmt.Sprintf("%v", res.Server.ID)); sidStr != "" && sidStr != "<nil>" {
			sid, sidOK = sidStr, true
		}
	}
	isp := nonDash(res.ISP)
	fmt.Fprintf(&sb, "Server      : %s\nCity        : %s\nID          : %s\nISP         : %s\n\n",
		orDash(server, serverOK), orDash(city, cityOK), orDash(sid, sidOK), orDash(isp, isp != "-"))

	if res.Ping != nil {
		fmt.Fprintf(&sb, "Latency     : %s\n- Jitter    : %s\n- Low       : %s\n- High      : %s\n\n",
			ms(res.Ping.Latency, true), ms(res.Ping.Jitter, true), ms(res.Ping.Low, true), ms(res.Ping.High, true))
	} else {
		fmt.Fprintf(&sb, "Latency     : -\n\n")
	}

	writeIO := func(label string, io *speedtestIO) {
		if io == nil {
			fmt.Fprintf(&sb, "%-12s: -\n\n", label)
			return
		}
		fmt.Fprintf(&sb, "%-12s: %s\n", label, formatRate(io.Bandwidth))
		if io.Latency != nil {
			fmt.Fprintf(&sb, "- Latency   : %s\n- Jitter    : %s\n",
				ms(io.Latency.IQM, true), ms(io.Latency.Jitter, true))
		} else {
			fmt.Fprintf(&sb, "- Latency   : -\n- Jitter    : -\n")
		}
		fmt.Fprintf(&sb, "- Data Used : %s\n\n", formatBytes(io.Bytes))
	}
	writeIO("Download", res.Download)
	writeIO("Upload", res.Upload)

	if res.PacketLoss != nil {
		fmt.Fprintf(&sb, "Packet Loss : %.1f%%", *res.PacketLoss)
	} else {
		fmt.Fprintf(&sb, "Packet Loss : -")
	}
	sb.WriteString("\n```")
	return sb.String()
}

// nonDash mengubah string kosong jadi "-" untuk tampilan.
func nonDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// orDash memilih teks bila ok, "-" bila tidak.
func orDash(s string, ok bool) string {
	if !ok {
		return "-"
	}
	return s
}

// formatRate mengubah byte/detik jadi Kbps/Mbps/Gbps 2 desimal.
func formatRate(bps float64) string {
	bps8 := bps * 8
	switch {
	case bps8 >= 1e9:
		return fmt.Sprintf("%.2f Gbps", bps8/1e9)
	case bps8 >= 1e6:
		return fmt.Sprintf("%.2f Mbps", bps8/1e6)
	case bps8 >= 1e3:
		return fmt.Sprintf("%.2f Kbps", bps8/1e3)
	default:
		return fmt.Sprintf("%.0f bps", bps8)
	}
}

// formatBytes mengubah byte jadi KB/MB/GB 2 desimal.
func formatBytes(b float64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MB", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2f KB", b/(1<<10))
	default:
		return fmt.Sprintf("%.0f B", b)
	}
}
