package geminiapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// buildStreamLine meniru bentuk baris balasan StreamGenerate: pembungkus
// wrb.fr yang berisi string JSON bersarang.
func buildStreamLine(t *testing.T, inner []any) string {
	t.Helper()
	raw, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	line, err := json.Marshal([]any{[]any{"wrb.fr", nil, string(raw), nil, nil, nil, "generic"}})
	if err != nil {
		t.Fatalf("marshal line: %v", err)
	}
	return string(line)
}

// streamInner merakit payload balasan dengan teks kumulatif + id percakapan.
func streamInner(texts []string, withImage bool) []any {
	snapshots := make([]any, 0, len(texts))
	for _, s := range texts {
		snapshots = append(snapshots, s)
	}
	block := make([]any, 13)
	block[0] = "rc_1"
	block[1] = snapshots
	if withImage {
		imgMeta := make([]any, 4)
		imgMeta[3] = []any{"https://lh3.googleusercontent.com/gg-dl/abc"}
		head := make([]any, 4)
		head[3] = imgMeta
		entry := []any{head}
		group := []any{[]any{entry}}
		holder := make([]any, 8)
		holder[7] = group
		block[12] = holder
	}
	return []any{
		nil,
		[]any{"c_abc", "r_def"},
		nil, nil,
		[]any{block},
	}
}

func TestParseStreamTextAndConv(t *testing.T) {
	line := buildStreamLine(t, streamInner([]string{"Halo", "Halo dunia"}, false))
	res, err := parseStream(strings.NewReader(")]}'\n\n" + line + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "Halo dunia" {
		t.Fatalf("teks = %q, mau %q", res.Text, "Halo dunia")
	}
	if res.Conv == nil {
		t.Fatal("conv nil")
	}
	if res.Conv.ID != "c_abc" || res.Conv.ResponseID != "r_def" || res.Conv.ChoiceID != "rc_1" {
		t.Fatalf("conv = %+v", res.Conv)
	}
	if len(res.Images) != 0 {
		t.Fatalf("gambar tak diharapkan: %v", res.Images)
	}
}

func TestParseStreamImage(t *testing.T) {
	line := buildStreamLine(t, streamInner([]string{"Ini gambarnya."}, true))
	res, err := parseStream(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Images) != 1 {
		t.Fatalf("gambar = %d, mau 1", len(res.Images))
	}
	if got := res.Images[0]; !strings.HasSuffix(got, "=s2048") {
		t.Fatalf("url gambar tanpa ukuran: %q", got)
	}
}

// TestParseStreamImageCandidates memastikan pemindai tidak hanya mengambil
// URL pertama: stream kadang memuat beberapa bentuk (preview dan URL penuh),
// dan diagnostik butuh semuanya.
func TestParseStreamImageCandidates(t *testing.T) {
	inner := streamInner([]string{"Ini gambarnya."}, true)
	holder := inner[4].([]any)[0].([]any)[12].([]any)
	group := holder[7].([]any)
	entry := group[0].([]any)[0].([]any)
	head := entry[0].([]any)
	meta := head[3].([]any)
	meta[3] = []any{
		"https://lh3.googleusercontent.com/gg-dl/pendek",
		"https://lh3.googleusercontent.com/gg-dl/panjang-sekali",
		"https://example.com/bukan-media",
	}
	line := buildStreamLine(t, inner)
	res, err := parseStream(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("kandidat = %d (%v), mau 2", len(res.Candidates), res.Candidates)
	}
	if len(res.Images) != 1 {
		t.Fatalf("gambar = %d, mau 1", len(res.Images))
	}
}

func TestCollectMediaCandidatesFiltersAndDedupes(t *testing.T) {
	payload := []any{
		nil,
		[]any{
			"https://lh3.googleusercontent.com/gg-dl/abc",
			[]any{"https://lh3.googleusercontent.com/gg-dl/abc", "bukan url"},
			"https://work.fife.usercontent.google.com/rd-gg-dl/def",
			"https://example.com/gambar.png",
		},
	}
	got := collectMediaCandidates(payload, 0)
	if len(got) != 3 {
		t.Fatalf("kandidat = %v, mau 3 (duplikat belum disaring di sini)", got)
	}
	seen := map[string]bool{}
	for _, u := range got {
		seen[u] = true
	}
	if !seen["https://lh3.googleusercontent.com/gg-dl/abc"] {
		t.Fatalf("URL lh3 hilang: %v", got)
	}
	if !seen["https://work.fife.usercontent.google.com/rd-gg-dl/def"] {
		t.Fatalf("URL fife hilang: %v", got)
	}
	for _, u := range got {
		if strings.Contains(u, "example.com") {
			t.Fatalf("host non-Google lolos: %v", got)
		}
	}
}

func TestContainsString(t *testing.T) {
	list := []string{"a", "b"}
	if !containsString(list, "b") {
		t.Fatal("b tidak ditemukan")
	}
	if containsString(list, "c") {
		t.Fatal("c seharusnya tidak ada")
	}
}

// TestParseStreamCleanup memastikan penanda internal Gemini tidak ikut
// terkirim ke WhatsApp.
func TestParseStreamCleanup(t *testing.T) {
	text := "Ini hasilnya http://googleusercontent.com/image_generation_content/0_785[cite: 1] selesai."
	line := buildStreamLine(t, streamInner([]string{text}, false))
	res, err := parseStream(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(res.Text, "googleusercontent") || strings.Contains(res.Text, "cite:") {
		t.Fatalf("penanda internal masih ada: %q", res.Text)
	}
	if res.Text != "Ini hasilnya selesai." {
		t.Fatalf("teks = %q", res.Text)
	}
}

func TestParseStreamIgnoresGarbage(t *testing.T) {
	body := ")]}'\n\n" + `["di'o",]` + "\n" + `[["wrb.fr",null,null,null,null,null,"generic"]]` + "\n"
	res, err := parseStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "" || len(res.Images) != 0 {
		t.Fatalf("harus kosong, dapat %q / %d gambar", res.Text, len(res.Images))
	}
}

func TestParseStreamStaleConversation(t *testing.T) {
	line := `[["wrb.fr",null,null,null,null,[["BardErrorInfo",[1096]]],"generic"]]`
	_, err := parseStream(strings.NewReader(line + "\n"))
	if !errors.Is(err, ErrStaleConv) {
		t.Fatalf("error = %v, mau ErrStaleConv", err)
	}
}

func TestBuildChatRequestShape(t *testing.T) {
	raw := buildChatRequest(Request{
		Prompt: "halo",
		Model:  "pro",
		Conv:   &Conversation{ID: "c_1", ResponseID: "r_1", ChoiceID: "rc_1", Turn: 2},
		Files:  []File{{ID: "/contrib_service/ttl_1d/x", Name: "a.png"}},
	}, "id")

	var outer []any
	if err := json.Unmarshal([]byte(raw), &outer); err != nil {
		t.Fatalf("f.req bukan JSON: %v", err)
	}
	if len(outer) != 2 || outer[0] != nil {
		t.Fatalf("f.req luar = %v", outer)
	}
	innerStr, ok := outer[1].(string)
	if !ok {
		t.Fatalf("slot 1 bukan string: %T", outer[1])
	}
	var req []any
	if err := json.Unmarshal([]byte(innerStr), &req); err != nil {
		t.Fatalf("payload dalam bukan JSON: %v", err)
	}
	if len(req) != 97 {
		t.Fatalf("panjang payload = %d, mau 97", len(req))
	}
	first, _ := req[0].([]any)
	if len(first) == 0 || first[0] != "halo" {
		t.Fatalf("slot 0 = %v", req[0])
	}
	files, _ := first[3].([]any)
	if len(files) != 1 {
		t.Fatalf("lampiran = %v", first[3])
	}
	ids, _ := req[2].([]any)
	if len(ids) < 3 || ids[0] != "c_1" || ids[1] != "r_1" || ids[2] != "rc_1" {
		t.Fatalf("slot 2 = %v", req[2])
	}
	if req[79] != float64(3) {
		t.Fatalf("slot 79 (mode pro) = %v", req[79])
	}
	if req[96] != float64(0) {
		t.Fatalf("slot 96 (lanjutan) = %v", req[96])
	}
}

func TestBuildChatRequestNewConversation(t *testing.T) {
	raw := buildChatRequest(Request{Prompt: "hai"}, "id")
	var outer []any
	if err := json.Unmarshal([]byte(raw), &outer); err != nil {
		t.Fatalf("f.req: %v", err)
	}
	var req []any
	if err := json.Unmarshal([]byte(outer[1].(string)), &req); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if req[96] != float64(1) {
		t.Fatalf("slot 96 giliran pertama = %v", req[96])
	}
	if req[79] != float64(1) {
		t.Fatalf("slot 79 flash = %v", req[79])
	}
}

func TestModelModeMapping(t *testing.T) {
	cases := map[string]int{"": 1, "flash": 1, "pro": 3, "lite": 6, "gemini-3.5-flash-lite": 6}
	for in, want := range cases {
		if got := modelMode(in); got != want {
			t.Fatalf("modelMode(%q) = %d, mau %d", in, got, want)
		}
	}
}
