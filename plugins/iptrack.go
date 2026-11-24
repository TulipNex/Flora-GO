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
		return store.ReplyText(ctx, mctx, Help(HelpSpec{
			Title:   "Lacak IP atau domain",
			Cmd:     "Iptrack",
			Formats: []string{".iptrack <ip|url>"},
			Contoh:  ".iptrack 8.8.8.8",
		}))
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
	b.WriteString(Title("IP Tracker") + "\n\n")

	// Target
	b.WriteString("`Target`\n")
	targetLines := make([]string, 0, 4)
	if host != "" {
		targetLines = append(targetLines, "- Host          : "+host)
	}
	targetLines = append(targetLines, "- IP            : "+ip)
	if info.ReverseDNS != "" {
		targetLines = append(targetLines, "- Reverse DNS   : "+info.ReverseDNS)
	}
	if info.RIR != "" {
		targetLines = append(targetLines, "- Registri(RIR) : "+info.RIR)
	}
	b.WriteString(Code(targetLines...) + "\n\n")

	// Geolokasi
	b.WriteString("`Geolokasi`\n")
	geoLines := make([]string, 0, 12)
	if info.City != "" {
		geoLines = append(geoLines, "- Kota         : "+info.City)
	}
	if info.State != "" {
		geoLines = append(geoLines, "- Provinsi     : "+info.State)
	}
	if info.Country != "" {
		c := info.Country
		if info.CountryCode != "" {
			c += fmt.Sprintf(" (%s)", info.CountryCode)
		}
		geoLines = append(geoLines, "- Negara       : "+c)
	}
	if info.Continent != "" {
		geoLines = append(geoLines, "- Benua        : "+info.Continent)
	}
	if info.CallingCode != "" {
		cc := info.CallingCode
		if !strings.HasPrefix(cc, "+") {
			cc = "+" + cc
		}
		geoLines = append(geoLines, "- Kode Telepon : "+cc)
	}
	if info.CurrencyCode != "" {
		geoLines = append(geoLines, "- Mata Uang    : "+info.CurrencyCode)
	}
	if info.Timezone != "" {
		geoLines = append(geoLines, "- Zona Waktu   : "+info.Timezone)
	}
	if info.UTCOffset != "" {
		geoLines = append(geoLines, "- UTC Offset   : "+info.UTCOffset)
	}
	if info.Zip != "" {
		geoLines = append(geoLines, "- Kode Pos     : "+info.Zip)
	}
	if info.Accuracy != "" {
		geoLines = append(geoLines, "- Akurasi      : "+info.Accuracy)
	}
	if info.IsEU {
		geoLines = append(geoLines, "- Anggota EU   : "+yesNoIP(info.IsEU))
	}
	if info.Latitude != 0 || info.Longitude != 0 {
		geoLines = append(geoLines, fmt.Sprintf("- Koordinat    : %.5f, %.5f", info.Latitude, info.Longitude))
	}
	b.WriteString(Code(geoLines...) + "\n\n")

	// Perusahaan
	if info.CompanyName != "" || info.CompanyDomain != "" || info.CompanyNetwork != "" {
		b.WriteString("`Perusahaan`\n")
		compLines := make([]string, 0, 6)
		if info.CompanyName != "" {
			compLines = append(compLines, "- Nama         : "+info.CompanyName)
		}
		if info.CompanyType != "" {
			compLines = append(compLines, "- Tipe         : "+info.CompanyType)
		}
		if info.CompanyDomain != "" {
			compLines = append(compLines, "- Domain       : "+info.CompanyDomain)
		}
		if info.CompanyNetwork != "" {
			compLines = append(compLines, "- Jaringan     : "+info.CompanyNetwork)
		}
		if info.CompanyNetname != "" {
			compLines = append(compLines, "- Netname      : "+info.CompanyNetname)
		}
		if info.CompanyAbuserScore != "" {
			compLines = append(compLines, "- Skor Abuser  : "+info.CompanyAbuserScore)
		}
		b.WriteString(Code(compLines...) + "\n\n")
	}

	// ASN
	if info.ASN != 0 || info.ASNOrg != "" || info.ASNRoute != "" {
		b.WriteString("`ASN`\n")
		asnLines := make([]string, 0, 11)
		if info.ASN != 0 {
			asnLines = append(asnLines, fmt.Sprintf("- Nomor        : AS%d", info.ASN))
		}
		if info.ASNOrg != "" {
			asnLines = append(asnLines, "- Nama         : "+info.ASNOrg)
		}
		if info.ASNType != "" {
			asnLines = append(asnLines, "- Tipe         : "+info.ASNType)
		}
		if info.ASNRoute != "" {
			asnLines = append(asnLines, "- Rute         : "+info.ASNRoute)
		}
		if info.ASNCountry != "" {
			asnLines = append(asnLines, "- Negara       : "+info.ASNCountry)
		}
		if info.ASN != 0 {
			asnLines = append(asnLines, "- Status       : "+activeStatus(info.ASNActive))
		}
		if info.ASNDomain != "" {
			asnLines = append(asnLines, "- Domain       : "+info.ASNDomain)
		}
		if info.ASNAbuse != "" {
			asnLines = append(asnLines, "- Email Abuse  : "+info.ASNAbuse)
		}
		if info.ASNAbuserScore != "" {
			asnLines = append(asnLines, "- Skor Abuser  : "+info.ASNAbuserScore)
		}
		if info.ASNUpdated != "" {
			asnLines = append(asnLines, "- Diperbarui   : "+info.ASNUpdated)
		} else if info.ASNCreated != "" {
			asnLines = append(asnLines, "- Dibuat       : "+info.ASNCreated)
		}
		if info.ASNDescrip != "" {
			asnLines = append(asnLines, "- Deskripsi    : "+info.ASNDescrip)
		}
		b.WriteString(Code(asnLines...) + "\n\n")
	}

	// Datacenter
	if info.IsDatacenter && (info.DatacenterName != "" || info.DatacenterDomain != "" || info.DatacenterNetwork != "") {
		b.WriteString("`Datacenter`\n")
		dcLines := make([]string, 0, 3)
		if info.DatacenterName != "" {
			dcLines = append(dcLines, "- Nama         : "+info.DatacenterName)
		}
		if info.DatacenterDomain != "" {
			dcLines = append(dcLines, "- Domain       : "+info.DatacenterDomain)
		}
		if info.DatacenterNetwork != "" {
			dcLines = append(dcLines, "- Jaringan     : "+info.DatacenterNetwork)
		}
		b.WriteString(Code(dcLines...) + "\n\n")
	}

	// Keamanan
	b.WriteString("`Analisis Keamanan`\n")
	secLines := []string{
		"- VPN          : " + yesNoIP(info.IsVPN),
		"- Proxy        : " + yesNoIP(info.IsProxy),
		"- Tor          : " + yesNoIP(info.IsTor),
		"- Datacenter   : " + yesNoIP(info.IsDatacenter),
		"- Seluler      : " + yesNoIP(info.IsMobile),
		"- Crawler/Bot  : " + yesNoIP(info.IsCrawler),
		"- Anycast      : " + yesNoIP(info.IsAnycast),
		"- Satelit      : " + yesNoIP(info.IsSatellite),
		"- Abuser       : " + yesNoIP(info.IsAbuser),
	}
	b.WriteString(Code(secLines...))

	// Kontak Abuse
	if info.AbuseName != "" || info.AbuseEmail != "" || info.AbusePhone != "" || info.AbuseAddress != "" {
		b.WriteString("\n\n`Kontak Abuse`\n")
		abuseLines := make([]string, 0, 4)
		if info.AbuseName != "" {
			abuseLines = append(abuseLines, "- Nama         : "+info.AbuseName)
		}
		if info.AbuseEmail != "" {
			abuseLines = append(abuseLines, "- Email        : "+info.AbuseEmail)
		}
		if info.AbusePhone != "" {
			abuseLines = append(abuseLines, "- Telepon      : "+info.AbusePhone)
		}
		if info.AbuseAddress != "" {
			abuseLines = append(abuseLines, "- Alamat       : "+info.AbuseAddress)
		}
		b.WriteString(Code(abuseLines...))
	}

	return b.String()
}

func activeStatus(b bool) string {
	if b {
		return "Aktif"
	}
	return "Nonaktif"
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