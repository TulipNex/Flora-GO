package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const playTestScript = `#playground
title: Promo Kopi
body: Diskon 20% untuk semua menu hari ini.
footer: Flora • Promo
image: https://contoh.com/promo.jpg
+ button url | Beli Sekarang | https://contoh.com/beli
+ button reply | Tanya Admin
`

func TestPlaygroundParseInteractive(t *testing.T) {
	sp, errs := playParse(playTestScript)
	if len(errs) != 0 {
		t.Fatalf("parse error: %v", errs)
	}
	if sp.kind != playKindInteractive {
		t.Fatalf("kind = %q, mau interactive", sp.kind)
	}
	if sp.title != "Promo Kopi" || sp.footer != "Flora • Promo" {
		t.Fatalf("field header/footer salah: %+v", sp)
	}
	if sp.image != "https://contoh.com/promo.jpg" {
		t.Fatalf("image = %q", sp.image)
	}
	if len(sp.buttons) != 2 {
		t.Fatalf("buttons = %d, mau 2", len(sp.buttons))
	}
	if b := sp.buttons[0]; b.typ != "url" || b.label != "Beli Sekarang" || b.arg != "https://contoh.com/beli" {
		t.Fatalf("tombol url salah: %+v", b)
	}
	if b := sp.buttons[1]; b.typ != "reply" || b.label != "Tanya Admin" || b.arg != "" {
		t.Fatalf("tombol reply salah: %+v", b)
	}
}

func TestPlaygroundMarker(t *testing.T) {
	if !playHasMarker("\n\n" + playMarker + "\ntitle: x") {
		t.Fatal("marker setelah baris kosong harus diterima")
	}
	if !playHasMarker(strings.ToUpper(playMarker)) {
		t.Fatal("marker harus case-insensitive")
	}
	if playHasMarker("title: x\n" + playMarker) {
		t.Fatal("marker di tengah script tidak boleh diterima")
	}
	if playHasMarker(playMarker + "x") {
		t.Fatal("marker dengan akhiran lain tidak boleh diterima")
	}
}

func TestPlaygroundParseErrors(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"tanpa marker", "title: x", "Baris pertama"},
		{"kunci asing", playMarker + "\nbodyy: x", "kunci `bodyy`"},
		{"baris asing", playMarker + "\nhalo dunia", "baris tidak dikenal"},
		{"tipe tombol asing", playMarker + "\n+ button nope | X", "tipe tombol"},
		{"row tanpa select", playMarker + "\n+ row | A", "harus ditulis setelah"},
		{"hapus berlebihan", playMarker + "\n- button 2", "tidak ada"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := playParse(tc.script)
			if len(errs) == 0 {
				t.Fatal("mau error, dapat kosong")
			}
			joined := strings.Join(errs, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("error %q tidak memuat %q", joined, tc.want)
			}
		})
	}
}

func TestPlaygroundDeleteButton(t *testing.T) {
	sp, errs := playParse(playMarker + "\nbody: x\n+ button reply | A\n+ button reply | B\n+ button reply | C\n- button 2")
	if len(errs) != 0 {
		t.Fatalf("parse error: %v", errs)
	}
	if len(sp.buttons) != 2 || sp.buttons[0].label != "A" || sp.buttons[1].label != "C" {
		t.Fatalf("hasil hapus salah: %+v", sp.buttons)
	}
}

func TestPlaygroundSplitEscaped(t *testing.T) {
	got := playSplit(`url | A \| B | https://x`)
	want := []string{"url", "A | B", "https://x"}
	if len(got) != len(want) {
		t.Fatalf("split = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bagian %d = %q, mau %q", i, got[i], want[i])
		}
	}
}

func TestPlaygroundValidate(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"body kosong", playMarker + "\n+ button reply | A", "`body:` wajib"},
		{"carousel tanpa kartu", playMarker + "\nbody: x\nslot: carousel", "minimal satu `+ card`"},
		{"tombol di luar kartu", playMarker + "\nbody: x\n+ button reply | A\n+ card | C | B", "di luar kartu"},
		{"kartu dengan slot buttons", playMarker + "\nbody: x\nslot: buttons\n+ card | C | B", "tidak terpakai"},
		{"label panjang", playMarker + "\nbody: x\n+ button url | " + strings.Repeat("A", 21) + " | https://x", "label 21 rune"},
		{"url salah", playMarker + "\nbody: x\n+ button url | A | ftp://x", "harus diawali http"},
		{"airich footer", playMarker + "\nkind: airich\nbody: x\nfooter: y", "tidak memakai `footer:`"},
		{"event tanpa judul", playMarker + "\nkind: event", "butuh `title:`"},
		{"phone dengan field", playMarker + "\nkind: phone\ntitle: x", "tidak memakai field lain"},
		{"kind asing", playMarker + "\nkind: video", "tidak dikenal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp, errs := playParse(tc.script)
			if len(errs) == 0 {
				errs = playValidate(&sp)
			}
			joined := strings.Join(errs, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("error %q tidak memuat %q", joined, tc.want)
			}
		})
	}
}

func TestPlaygroundBuildInteractive(t *testing.T) {
	sp, errs := playParse(playTestScript)
	if len(errs) != 0 {
		t.Fatalf("parse error: %v", errs)
	}
	if errs := playValidate(&sp); len(errs) != 0 {
		t.Fatalf("validate error: %v", errs)
	}
	msg, err := playBuild(context.Background(), sp, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	im := msg.GetInteractiveMessage()
	if im == nil {
		t.Fatal("bukan interactive message")
	}
	if got := im.GetBody().GetText(); got != sp.body {
		t.Fatalf("body = %q", got)
	}
	if got := im.GetHeader().GetTitle(); got != "Promo Kopi" {
		t.Fatalf("title = %q", got)
	}
	if got := im.GetFooter().GetText(); got != "Flora • Promo" {
		t.Fatalf("footer = %q", got)
	}

	nf := im.GetNativeFlowMessage()
	if nf == nil || len(nf.GetButtons()) != 2 {
		t.Fatalf("native flow buttons = %+v", nf)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(nf.GetButtons()[1].GetButtonParamsJSON()), &params); err != nil {
		t.Fatalf("params bukan JSON: %v", err)
	}
	if params["id"] != "play:2" || params["display_text"] != "Tanya Admin" {
		t.Fatalf("params reply = %+v", params)
	}
}

func TestPlaygroundBuildCarousel(t *testing.T) {
	script := playMarker + `
slot: carousel
body: Pilih paket.
+ card | Basic | Akses standar.
+ button url | Detail | https://contoh.com/basic
+ card | Premium | Semua fitur.
+ button url | Detail | https://contoh.com/premium
`
	sp, errs := playParse(script)
	if len(errs) != 0 {
		t.Fatalf("parse error: %v", errs)
	}
	if errs := playValidate(&sp); len(errs) != 0 {
		t.Fatalf("validate error: %v", errs)
	}
	msg, err := playBuild(context.Background(), sp, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cards := msg.GetInteractiveMessage().GetCarouselMessage().GetCards()
	if len(cards) != 2 {
		t.Fatalf("kartu = %d, mau 2", len(cards))
	}
	if cards[0].GetHeader().GetTitle() != "Basic" || cards[1].GetHeader().GetTitle() != "Premium" {
		t.Fatalf("judul kartu salah: %q / %q", cards[0].GetHeader().GetTitle(), cards[1].GetHeader().GetTitle())
	}
	for i, c := range cards {
		if n := len(c.GetNativeFlowMessage().GetButtons()); n != 1 {
			t.Fatalf("kartu %d punya %d tombol, mau 1", i+1, n)
		}
	}
}

func TestPlaygroundBuildA2UI(t *testing.T) {
	script := playMarker + `
body: Ringkasan.
+ a2ui heading | Dashboard
+ a2ui card | Memori | Alloc 18 MB
+ button url | Monitor | https://contoh.com
`
	sp, errs := playParse(script)
	if len(errs) != 0 {
		t.Fatalf("parse error: %v", errs)
	}
	if errs := playValidate(&sp); len(errs) != 0 {
		t.Fatalf("validate error: %v", errs)
	}
	msg, err := playBuild(context.Background(), sp, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	im := msg.GetInteractiveMessage()
	if im.GetBloksWidget() == nil {
		t.Fatal("bloks widget tidak terpasang")
	}
	if im.GetBloksWidget().GetType() != "im_a2ui" {
		t.Fatalf("tipe widget = %q", im.GetBloksWidget().GetType())
	}
	if data := im.GetBloksWidget().GetData(); !strings.Contains(data, "createSurface") || !strings.Contains(data, "Dashboard") {
		t.Fatalf("data A2UI tidak memuat surface/heading: %s", data)
	}
	if n := len(im.GetNativeFlowMessage().GetButtons()); n != 1 {
		t.Fatalf("tombol = %d, mau 1", n)
	}
}

func TestPlaygroundBuildAirich(t *testing.T) {
	script := playMarker + `
kind: airich
title: Portal
+ section md | Baca dokumentasi.
+ section link | Dokumentasi | https://go.dev/doc/
`
	sp, errs := playParse(script)
	if len(errs) != 0 {
		t.Fatalf("parse error: %v", errs)
	}
	if errs := playValidate(&sp); len(errs) != 0 {
		t.Fatalf("validate error: %v", errs)
	}
	msg, err := playBuild(context.Background(), sp, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	data := string(msg.GetBotForwardedMessage().GetMessage().GetRichResponseMessage().GetUnifiedResponse().GetData())
	if !strings.Contains(data, "Portal") || !strings.Contains(data, "GenAIFooterActionPrimitive") {
		t.Fatalf("payload airich tidak lengkap: %s", data)
	}
}

func TestPlaygroundBuildEventPhone(t *testing.T) {
	ev, err := playBuild(context.Background(), playSpec{kind: playKindEvent, title: "Meetup"}, nil, []byte{0xFF, 0xD8})
	if err != nil {
		t.Fatalf("build event: %v", err)
	}
	if got := ev.GetEventInviteMessage().GetEventTitle(); got != "Meetup" {
		t.Fatalf("judul event = %q", got)
	}
	if n := len(ev.GetEventInviteMessage().GetJPEGThumbnail()); n != 2 {
		t.Fatalf("thumbnail = %d byte", n)
	}

	phone, err := playBuild(context.Background(), playSpec{kind: playKindPhone}, nil, nil)
	if err != nil {
		t.Fatalf("build phone: %v", err)
	}
	if phone.GetRequestPhoneNumberMessage() == nil {
		t.Fatal("bukan RequestPhoneNumberMessage")
	}
}

func TestPlaygroundUsage(t *testing.T) {
	all := playUsage("")
	for _, want := range []string{playMarker, "*Alur*", "*Umum*", "*Tombol*", "*Carousel*", "*A2UI*", "*Airich*", "*Event*", "*Contoh*"} {
		if !strings.Contains(all, want) {
			t.Fatalf("referensi tidak memuat %q", want)
		}
	}
	filtered := playUsage("tombol")
	if !strings.Contains(filtered, "*Tombol*") {
		t.Fatal("filter tombol tidak menampilkan seksi tombol")
	}
	if strings.Contains(filtered, "*Carousel*") {
		t.Fatal("filter tombol masih menampilkan seksi carousel")
	}
}
