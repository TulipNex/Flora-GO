package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"wabot-go/internal/apicache"
	"wabot-go/internal/identity"
	"wabot-go/internal/store"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

func init() {
	registerPlugin(RegisterIPTrack)
}

// RegisterIPTrack mendaftarkan `.iptrack`: lookup informasi IP publik atau
// domain/URL (geolokasi, ASN, organisasi, datacenter, VPN/proxy/Tor, abuse).
// Kategori Internet, publik (tersedia di clone).
func RegisterIPTrack(r *Registry) {
	r.Register(Command{
		Name:        "iptrack",
		Aliases:     []string{"ip", "ipinfo", "iplookup"},
		Description: "Lacak info IP/domain (geo, ASN, VPN/proxy, abuse)",
		Category:    CategoryInternet,
		Handler:     handleIPTrack,
	})
}

// ---- model data ternormalisasi (menangani format gratis & lengkap) ----

type ipInfo struct {
	IP          string
	IsBogon     bool
	City        string
	State       string
	Country     string
	CountryCode string
	Continent   string
	IsEU        bool
	CallingCode string
	CurrencyCode string
	Latitude    float64
	Longitude   float64
	Zip         string
	Timezone    string
	UTCOffset   string
	Accuracy    string
	RIR         string
	RawDocs     string

	// ASN
	ASN         int
	ASNOrg      string
	ASNType     string
	ASNRoute    string
	ASNCountry  string
	ASNActive   bool
	ASNAbuserScore string
	ASNCreated  string
	ASNUpdated  string
	ASNDescrip  string
	ASNDomain   string
	ASNAbuse    string

	// organisasi
	CompanyName      string
	CompanyType      string
	CompanyDomain    string
	CompanyNetwork   string
	CompanyNetname   string
	CompanyAbuserScore string

	// datacenter
	DatacenterName    string
	DatacenterDomain  string
	DatacenterNetwork string

	// abuse contact
	AbuseName    string
	AbuseAddress string
	AbuseEmail   string
	AbusePhone   string

	// keamanan
	IsMobile     bool
	IsSatellite  bool
	IsAnycast    bool
	IsCrawler    bool
	IsDatacenter bool
	IsTor        bool
	IsProxy      bool
	IsVPN        bool
	IsAbuser     bool

	ReverseDNS string
}

// ---- cache & cooldown ----

// ipTrackCache menyimpan hasil lookup per IP: TTL 1 jam + kapasitas 512 entri
// (dulu sync.Map tanpa batas, sehingga tumbuh seumur proses untuk command
// publik). singleflight di dalamnya juga mencegah lookup ganda saat dua user
// meminta IP yang sama bersamaan.
var ipTrackCache = apicache.New[ipInfo](time.Hour, 512)

// ipTrackCooldown: kunci user (user-part, bukan string JID) -> waktu boleh lagi.
var ipTrackCooldown sync.Map

// ipTrackCooldownKeys menghasilkan kunci cooldown untuk pengirim. Memakai
// user-part (bukan string JID mentah) supaya @lid dan @pn untuk orang yang
// sama tidak menjadi dua kunci berbeda yang melewati jeda 5 detik.
func ipTrackCooldownKeys(mctx *store.MessageContext) []string {
	var out []string
	seen := map[string]bool{}
	add := func(j types.JID) {
		if u := identity.UserPart(j); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	add(mctx.Sender)
	if mctx.RawEvt != nil {
		add(mctx.RawEvt.Info.SenderAlt)
	}
	if len(out) == 0 {
		out = append(out, mctx.Sender.String())
	}
	return out
}

func ipTrackCooldownLeft(keys []string) time.Duration {
	var worst time.Duration
	for _, k := range keys {
		v, ok := ipTrackCooldown.Load(k)
		if !ok {
			continue
		}
		t, _ := v.(time.Time)
		if rem := time.Until(t); rem > worst {
			worst = rem
		}
	}
	return worst
}

func ipTrackCooldownSet(keys []string) {
	until := time.Now().Add(5 * time.Second)
	for _, k := range keys {
		ipTrackCooldown.Store(k, until)
	}
}

// ---- handler ----

func handleIPTrack(ctx context.Context, mctx *store.MessageContext) error {
	input := strings.TrimSpace(mctx.RawArgs)
	if input == "" {
		return store.ReplyText(ctx, mctx,
			"Pemakaian: .iptrack <ip|url/domain>\nContoh: .iptrack 8.8.8.8 | .iptrack example.com")
	}

	ip, host, err := resolveIPTrackTarget(input)
	if err != nil {
		return store.ReplyText(ctx, mctx, "Input tidak valid: "+err.Error()+"\nContoh: .iptrack 8.8.8.8 | .iptrack https://example.com")
	}

	senderKeys := ipTrackCooldownKeys(mctx)
	if rem := ipTrackCooldownLeft(senderKeys); rem > 0 {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("Tunggu %.0f detik sebelum lookup lagi.", rem.Seconds()))
	}
	ipTrackCooldownSet(senderKeys)

	info, err := ipTrackCache.GetOrLoad(ctx, ip, func(c context.Context) (ipInfo, error) {
		return lookupIPTrack(c, ip)
	})
	if err != nil {
		return store.ReplyText(ctx, mctx, "Gagal mengambil data IP: "+err.Error())
	}

	return sendIPTrackResult(ctx, mctx, ip, host, info)
}

func resolveIPTrackTarget(input string) (ip, host string, err error) {
	host = input
	if strings.Contains(input, "://") {
		u, perr := url.Parse(input)
		if perr != nil || u.Hostname() == "" {
			return "", "", fmt.Errorf("URL tidak valid")
		}
		host = u.Hostname()
	} else if i := strings.Index(input, "/"); i >= 0 {
		host = strings.TrimSpace(input[:i])
	}

	if parsed := net.ParseIP(host); parsed != nil {
		return parsed.String(), "", nil
	}
	ips, lerr := net.LookupIP(host)
	if lerr != nil {
		return "", "", fmt.Errorf("gagal resolve domain: %w", lerr)
	}
	if len(ips) == 0 {
		return "", "", fmt.Errorf("domain tidak memiliki IP")
	}
	chosen := ips[0]
	for _, candidate := range ips {
		if candidate.To4() != nil {
			chosen = candidate
			break
		}
	}
	return chosen.String(), host, nil
}

func ipapiKey() string {
	for _, k := range []string{"IPAPI_IS_KEY", "IPAPIIS_API_KEY", "IPAPI_IS_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func lookupIPTrack(ctx context.Context, ip string) (ipInfo, error) {
	key := ipapiKey()
	u := "https://api.ipapi.is?q=" + url.QueryEscape(ip)
	if key != "" {
		u += "&key=" + url.QueryEscape(key)
	}
	raw, err := fetchJSONMap(ctx, u)
	if err != nil {
		return ipInfo{}, err
	}
	info := normalizeIPAPIIS(raw)
	if info.IP == "" {
		info.IP = ip
	}
	if info.IsBogon {
		return info, nil
	}
	// mode gratis: lengkapi field keamanan dari ip-api.com (tanpa key)
	if key == "" {
		enrichWithIPAPI(ctx, &info, ip)
	}
	return info, nil
}

func fetchJSONMap(ctx context.Context, u string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "wabot-go/iptrack")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- normalisasi ipapi.is (gratis ringkas ATAU lengkap dengan key) ----

func normalizeIPAPIIS(raw map[string]any) ipInfo {
	info := ipInfo{}
	info.IP = asString(raw["ip"])
	info.IsBogon = asBool(raw["is_bogon"])
	info.RIR = asString(raw["rir"])
	info.RawDocs = asString(raw["docs"])

	// format gratis ringkas
	if s, ok := raw["company"].(string); ok {
		info.CompanyName = s
	}
	if s, ok := raw["asn"].(string); ok {
		parts := strings.Fields(s)
		if len(parts) > 0 {
			info.ASN, _ = strconv.Atoi(strings.TrimPrefix(strings.ToUpper(parts[0]), "AS"))
			info.ASNOrg = strings.Join(parts[1:], " ")
		}
	}
	info.City = asString(raw["city"])
	info.State = asString(raw["region"])
	info.Country = asString(raw["country"])
	info.Latitude = asFloat(raw["lat"])
	info.Longitude = asFloat(raw["lon"])
	info.Timezone = asString(raw["timezone"])

	// format lengkap (nested)
	if loc := asMap(raw["location"]); loc != nil {
		info.City = firstNonEmpty(asString(loc["city"]), info.City)
		info.State = firstNonEmpty(asString(loc["state"]), info.State)
		info.Country = firstNonEmpty(asString(loc["country"]), info.Country)
		info.CountryCode = asString(loc["country_code"])
		info.Continent = asString(loc["continent"])
		info.IsEU = asBool(loc["is_eu_member"])
		info.CallingCode = asString(loc["calling_code"])
		info.CurrencyCode = asString(loc["currency_code"])
		info.Latitude = firstNonZero(asFloat(loc["latitude"]), info.Latitude)
		info.Longitude = firstNonZero(asFloat(loc["longitude"]), info.Longitude)
		info.Timezone = firstNonEmpty(asString(loc["timezone"]), info.Timezone)
		info.UTCOffset = asString(loc["utcoffset"])
		info.Accuracy = asString(loc["accuracy"])
	}
	if c := asMap(raw["company"]); c != nil {
		info.CompanyName = firstNonEmpty(asString(c["name"]), info.CompanyName)
		info.CompanyAbuserScore = asString(c["abuser_score"])
		info.CompanyDomain = asString(c["domain"])
		info.CompanyType = asString(c["type"])
		info.CompanyNetwork = asString(c["network"])
		info.CompanyNetname = asString(c["netname"])
	}
	if a := asMap(raw["asn"]); a != nil {
		if n := asInt(a["asn"]); n != 0 {
			info.ASN = n
		}
		info.ASNAbuserScore = asString(a["abuser_score"])
		info.ASNRoute = asString(a["route"])
		info.ASNDescrip = asString(a["descr"])
		info.ASNCountry = asString(a["country"])
		info.ASNActive = asBool(a["active"])
		info.ASNOrg = firstNonEmpty(asString(a["org"]), info.ASNOrg)
		info.ASNType = asString(a["type"])
		info.ASNCreated = asString(a["created"])
		info.ASNUpdated = asString(a["updated"])
		info.RIR = firstNonEmpty(asString(a["rir"]), info.RIR)
		info.ASNAbuse = asString(a["abuse"])
		info.ASNDomain = asString(a["domain"])
	}
	if dc := asMap(raw["datacenter"]); dc != nil {
		info.DatacenterName = asString(dc["datacenter"])
		info.DatacenterDomain = asString(dc["domain"])
		info.DatacenterNetwork = asString(dc["network"])
	}
	if ab := asMap(raw["abuse"]); ab != nil {
		info.AbuseName = asString(ab["name"])
		info.AbuseAddress = asString(ab["address"])
		info.AbuseEmail = asString(ab["email"])
		info.AbusePhone = asString(ab["phone"])
	}

	// flags (format lengkap)
	info.IsMobile = asBool(raw["is_mobile"])
	info.IsSatellite = asBool(raw["is_satellite"])
	info.IsAnycast = asBool(raw["is_anycast"])
	info.IsCrawler = asBool(raw["is_crawler"])
	info.IsDatacenter = asBool(raw["is_datacenter"])
	info.IsTor = asBool(raw["is_tor"])
	info.IsProxy = asBool(raw["is_proxy"])
	info.IsVPN = asBool(raw["is_vpn"])
	info.IsAbuser = asBool(raw["is_abuser"])
	if info.RIR == "" {
		info.RIR = asString(raw["rir"])
	}
	return info
}

// ---- pengayaan dari ip-api.com (tanpa key) ----

type ipTrackIPAPIResponse struct {
	Status      string  `json:"status"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	RegionName  string  `json:"regionName"`
	City        string  `json:"city"`
	Zip         string  `json:"zip"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Timezone    string  `json:"timezone"`
	Isp         string  `json:"isp"`
	Org         string  `json:"org"`
	As          string  `json:"as"`
	Asname      string  `json:"asname"`
	Reverse     string  `json:"reverse"`
	Mobile      bool    `json:"mobile"`
	Proxy       bool    `json:"proxy"`
	Hosting     bool    `json:"hosting"`
}

func enrichWithIPAPI(ctx context.Context, info *ipInfo, ip string) {
	// SENGAJA http:// (bukan https): endpoint gratis ip-api.com hanya
	// melayani HTTP — HTTPS berbayar dan membalas 403 untuk kunci gratis.
	// Data di sini hanya pelengkap (flag proxy/hosting/mobile); sumber utama
	// tetap ipapi.is lewat HTTPS. Jangan "perbaiki" jadi https tanpa kunci.
	u := "http://ip-api.com/json/" + url.PathEscape(ip) + "?fields=status,country,countryCode,regionName,city,zip,lat,lon,timezone,isp,org,as,asname,reverse,mobile,proxy,hosting"
	raw, err := fetchJSONMap(ctx, u)
	if err != nil {
		return
	}
	// ip-api mengembalikan field datar; decode manual
	var r ipTrackIPAPIResponse
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &r)
	if r.Status != "success" {
		return
	}
	if info.City == "" {
		info.City = r.City
	}
	if info.State == "" {
		info.State = r.RegionName
	}
	if info.Country == "" {
		info.Country = r.Country
	}
	if info.CountryCode == "" {
		info.CountryCode = r.CountryCode
	}
	if info.Latitude == 0 {
		info.Latitude = r.Lat
	}
	if info.Longitude == 0 {
		info.Longitude = r.Lon
	}
	if info.Timezone == "" {
		info.Timezone = r.Timezone
	}
	if info.Zip == "" {
		info.Zip = r.Zip
	}
	if info.CompanyName == "" {
		info.CompanyName = r.Org
		if info.CompanyName == "" {
			info.CompanyName = r.Isp
		}
	}
	if info.ASN == 0 && r.As != "" {
		parts := strings.Fields(r.As)
		if len(parts) > 0 {
			info.ASN, _ = strconv.Atoi(strings.TrimPrefix(strings.ToUpper(parts[0]), "AS"))
			if info.ASNOrg == "" {
				info.ASNOrg = strings.Join(parts[1:], " ")
			}
		}
	}
	if info.ASNType == "" && r.Asname != "" {
		info.ASNType = r.Asname
	}
	// ip-api menyediakan flag keamanan (ipapi.is gratis tidak)
	info.IsProxy = r.Proxy
	info.IsDatacenter = r.Hosting
	info.IsMobile = r.Mobile
	if info.ReverseDNS == "" {
		info.ReverseDNS = r.Reverse
	}
}

// ---- kirim hasil ----

func sendIPTrackResult(ctx context.Context, mctx *store.MessageContext, ip, host string, info ipInfo) error {
	if info.IsBogon {
		return store.ReplyText(ctx, mctx, fmt.Sprintf("IP %s adalah alamat privat/reserved, tidak ada data geolokasi.", ip))
	}

	name := "Lokasi IP: " + ip
	if host != "" {
		name = host + " (" + ip + ")"
	}
	address := joinNonEmpty(", ", info.City, info.State, info.Country)

	if info.Latitude != 0 || info.Longitude != 0 {
		locMsg := &waE2E.Message{
			LocationMessage: &waE2E.LocationMessage{
				DegreesLatitude:  proto.Float64(info.Latitude),
				DegreesLongitude: proto.Float64(info.Longitude),
				Name:             proto.String(name),
				Address:          proto.String(address),
			},
		}
		if _, err := mctx.Client.SendMessage(ctx, mctx.Chat, locMsg); err == nil {
			store.LogOutgoing(mctx, "iptrack", "location "+ip)
		}
	}

	text := buildIPTrackText(ip, host, info)
	return store.ReplyText(ctx, mctx, text)
}

func buildIPTrackText(ip, host string, info ipInfo) string {
	var b strings.Builder
	b.WriteString("🌐 *IP TRACKER*\n")

	// Target
	b.WriteString("\n📌 *Target*\n")
	if host != "" {
		b.WriteString(fmt.Sprintf("• Host : %s\n", host))
	}
	b.WriteString(fmt.Sprintf("• IP : %s\n", ip))
	if info.ReverseDNS != "" {
		b.WriteString(fmt.Sprintf("• Reverse DNS : %s\n", info.ReverseDNS))
	}
	if info.RIR != "" {
		b.WriteString(fmt.Sprintf("• Registri (RIR) : %s\n", info.RIR))
	}

	// Geolokasi
	b.WriteString("\n📍 *Geolokasi*\n")
	if info.City != "" {
		b.WriteString(fmt.Sprintf("• Kota : %s\n", info.City))
	}
	if info.State != "" {
		b.WriteString(fmt.Sprintf("• Provinsi / Wilayah : %s\n", info.State))
	}
	if info.Country != "" {
		b.WriteString(fmt.Sprintf("• Negara : %s\n", info.Country))
	}
	if info.CountryCode != "" {
		b.WriteString(fmt.Sprintf("• Kode Negara : %s\n", info.CountryCode))
	}
	if info.Continent != "" {
		b.WriteString(fmt.Sprintf("• Benua : %s\n", info.Continent))
	}
	b.WriteString(fmt.Sprintf("• Anggota EU : %s\n", statusBadge(info.IsEU)))
	if info.Zip != "" {
		b.WriteString(fmt.Sprintf("• Kode Pos : %s\n", info.Zip))
	}
	if info.Latitude != 0 || info.Longitude != 0 {
		b.WriteString(fmt.Sprintf("• Garis Lintang : %.5f\n", info.Latitude))
		b.WriteString(fmt.Sprintf("• Garis Bujur : %.5f\n", info.Longitude))
	}
	if info.Accuracy != "" {
		b.WriteString(fmt.Sprintf("• Akurasi : %s\n", info.Accuracy))
	}
	if info.Timezone != "" {
		b.WriteString(fmt.Sprintf("• Zona Waktu : %s\n", info.Timezone))
	}
	if info.UTCOffset != "" {
		b.WriteString(fmt.Sprintf("• UTC Offset : %s\n", info.UTCOffset))
	}
	if info.CurrencyCode != "" {
		b.WriteString(fmt.Sprintf("• Mata Uang : %s\n", info.CurrencyCode))
	}
	if info.CallingCode != "" {
		b.WriteString(fmt.Sprintf("• Kode Telepon : %s\n", info.CallingCode))
	}

	// Organisasi & ASN
	hasOrg := info.CompanyName != "" || info.ASN != 0
	if hasOrg {
		b.WriteString("\n🏢 *Organisasi & ASN*\n")
		if info.CompanyName != "" {
			b.WriteString(fmt.Sprintf("• Perusahaan : %s\n", info.CompanyName))
		}
		if info.CompanyType != "" {
			b.WriteString(fmt.Sprintf("• Tipe Perusahaan : %s\n", info.CompanyType))
		}
		if info.CompanyDomain != "" {
			b.WriteString(fmt.Sprintf("• Domain Perusahaan : %s\n", info.CompanyDomain))
		}
		if info.CompanyNetwork != "" {
			b.WriteString(fmt.Sprintf("• Jaringan Perusahaan : %s\n", info.CompanyNetwork))
		}
		if info.CompanyNetname != "" {
			b.WriteString(fmt.Sprintf("• Netname Perusahaan : %s\n", info.CompanyNetname))
		}
		if info.CompanyAbuserScore != "" {
			b.WriteString(fmt.Sprintf("• Skor Abuser Perusahaan : %s\n", info.CompanyAbuserScore))
		}

		if info.ASN != 0 {
			b.WriteString(fmt.Sprintf("• Nomor ASN : AS%d\n", info.ASN))
		}
		if info.ASNOrg != "" {
			b.WriteString(fmt.Sprintf("• Nama ASN : %s\n", info.ASNOrg))
		}
		if info.ASNType != "" {
			b.WriteString(fmt.Sprintf("• Tipe ASN : %s\n", info.ASNType))
		}
		if info.ASNRoute != "" {
			b.WriteString(fmt.Sprintf("• Rute ASN : %s\n", info.ASNRoute))
		}
		if info.ASNCountry != "" {
			b.WriteString(fmt.Sprintf("• Negara ASN : %s\n", info.ASNCountry))
		}
		if info.ASN != 0 {
			b.WriteString(fmt.Sprintf("• Status ASN : %s\n", yesNoIP(info.ASNActive)))
		}
		if info.ASNDomain != "" {
			b.WriteString(fmt.Sprintf("• Domain ASN : %s\n", info.ASNDomain))
		}
		if info.ASNAbuse != "" {
			b.WriteString(fmt.Sprintf("• Email Abuse ASN : %s\n", info.ASNAbuse))
		}
		if info.ASNAbuserScore != "" {
			b.WriteString(fmt.Sprintf("• Skor Abuser ASN : %s\n", info.ASNAbuserScore))
		}
		if info.ASNCreated != "" {
			b.WriteString(fmt.Sprintf("• ASN Dibuat : %s\n", info.ASNCreated))
		}
		if info.ASNUpdated != "" {
			b.WriteString(fmt.Sprintf("• ASN Diperbarui : %s\n", info.ASNUpdated))
		}
		if info.ASNDescrip != "" {
			b.WriteString(fmt.Sprintf("• Deskripsi ASN : %s\n", info.ASNDescrip))
		}
	}

	// Datacenter
	if info.IsDatacenter && (info.DatacenterName != "" || info.DatacenterDomain != "" || info.DatacenterNetwork != "") {
		b.WriteString("\n🖥️ *Datacenter*\n")
		if info.DatacenterName != "" {
			b.WriteString(fmt.Sprintf("• Nama Datacenter : %s\n", info.DatacenterName))
		}
		if info.DatacenterDomain != "" {
			b.WriteString(fmt.Sprintf("• Domain Datacenter : %s\n", info.DatacenterDomain))
		}
		if info.DatacenterNetwork != "" {
			b.WriteString(fmt.Sprintf("• Jaringan Datacenter : %s\n", info.DatacenterNetwork))
		}
	}

	// Keamanan
	b.WriteString("\n🛡️ *Analisis Keamanan*\n")
	b.WriteString(fmt.Sprintf("• VPN : %s\n", statusBadge(info.IsVPN)))
	b.WriteString(fmt.Sprintf("• Proxy : %s\n", statusBadge(info.IsProxy)))
	b.WriteString(fmt.Sprintf("• Tor Exit Node : %s\n", statusBadge(info.IsTor)))
	b.WriteString(fmt.Sprintf("• Datacenter / Hosting : %s\n", statusBadge(info.IsDatacenter)))
	b.WriteString(fmt.Sprintf("• Jaringan Seluler : %s\n", statusBadge(info.IsMobile)))
	b.WriteString(fmt.Sprintf("• Crawler / Bot : %s\n", statusBadge(info.IsCrawler)))
	b.WriteString(fmt.Sprintf("• Anycast : %s\n", statusBadge(info.IsAnycast)))
	b.WriteString(fmt.Sprintf("• Satelit : %s\n", statusBadge(info.IsSatellite)))
	b.WriteString(fmt.Sprintf("• Riwayat Abuser : %s\n", statusBadge(info.IsAbuser)))

	// Kontak Abuse
	if info.AbuseName != "" || info.AbuseEmail != "" || info.AbusePhone != "" || info.AbuseAddress != "" {
		b.WriteString("\n⚠️ *Kontak Abuse*\n")
		if info.AbuseName != "" {
			b.WriteString(fmt.Sprintf("• Nama : %s\n", info.AbuseName))
		}
		if info.AbuseEmail != "" {
			b.WriteString(fmt.Sprintf("• Email : %s\n", info.AbuseEmail))
		}
		if info.AbusePhone != "" {
			b.WriteString(fmt.Sprintf("• Telepon : %s\n", info.AbusePhone))
		}
		if info.AbuseAddress != "" {
			b.WriteString(fmt.Sprintf("• Alamat : %s\n", info.AbuseAddress))
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

func statusBadge(b bool) string {
	if b {
		return "✅ Ya"
	}
	return "❌ Tidak"
}

// ---- helpers ----

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return strconv.Itoa(int(t))
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case int:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		return 0
	}
}

func asInt(v any) int {
	return int(asFloat(v))
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func firstNonZero(a, b float64) float64 {
	if a != 0 {
		return a
	}
	return b
}

func joinNonEmpty(sep string, parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func yesNoIP(b bool) string {
	if b {
		return "Ya"
	}
	return "Tidak"
}