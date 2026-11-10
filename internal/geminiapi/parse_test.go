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

// streamInner merakit payload balasan mode tamu: teks kumulatif di blok
// pertama payload[4][0][1].
func streamInner(texts []string) []any {
	snapshots := make([]any, 0, len(texts))
	for _, s := range texts {
		snapshots = append(snapshots, s)
	}
	block := make([]any, 13)
	block[0] = "rc_1"
	block[1] = snapshots
	return []any{
		nil,
		[]any{"c_abc", "r_def"},
		nil, nil,
		[]any{block},
	}
}

// TestParseStreamLongestText memastikan teks KUMULATIF diambil versi
// terpanjang. Potongan awal stream sering terpotong ("M" untuk "Merah").
func TestParseStreamLongestText(t *testing.T) {
	line := buildStreamLine(t, streamInner([]string{"M", "Merah, kuning", "Merah, kuning, biru."}))
	res, err := parseStream(strings.NewReader(")]}'\n\n" + line + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "Merah, kuning, biru." {
		t.Fatalf("teks = %q", res.Text)
	}
}

// TestParseStreamTextArray memastikan blok berbentuk array string juga dibaca
// (bentuk ini dipakai sebagian balasan).
func TestParseStreamTextArray(t *testing.T) {
	inner := streamInner(nil)
	inner[4].([]any)[0].([]any)[1] = []any{"Halo", "Halo dunia"}
	line := buildStreamLine(t, inner)
	res, err := parseStream(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "Halo dunia" {
		t.Fatalf("teks = %q", res.Text)
	}
}

// TestParseStreamCleanup memastikan penanda internal Gemini tidak ikut
// terkirim ke WhatsApp.
func TestParseStreamCleanup(t *testing.T) {
	text := "Ini hasilnya http://googleusercontent.com/image_generation_content/0_785[cite: 1] selesai."
	line := buildStreamLine(t, streamInner([]string{text}))
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
	if res.Text != "" {
		t.Fatalf("harus kosong, dapat %q", res.Text)
	}
}

func TestParseStreamStaleConversation(t *testing.T) {
	for _, code := range []string{"1096", "1097"} {
		line := `[["wrb.fr",null,null,null,null,[["BardErrorInfo",[` + code + `]]],"generic"]]`
		_, err := parseStream(strings.NewReader(line + "\n"))
		if !errors.Is(err, ErrStaleConv) {
			t.Fatalf("kode %s: error = %v, mau ErrStaleConv", code, err)
		}
	}
}

// TestParseStreamErrorTypePrefixed meniru nama tipe asli dari server
// (type.googleapis.com/...BardErrorInfo) — pencocokan harus memakai akhiran.
func TestParseStreamErrorTypePrefixed(t *testing.T) {
	line := `[["wrb.fr",null,null,null,null,[["type.googleapis.com/google.ai.BardErrorInfo",[1097,"can't continue"]]],"generic"]]`
	_, err := parseStream(strings.NewReader(line + "\n"))
	if !errors.Is(err, ErrStaleConv) {
		t.Fatalf("error = %v, mau ErrStaleConv", err)
	}
}

func TestParseStreamLimitError(t *testing.T) {
	line := `[["wrb.fr",null,null,null,null,[["BardErrorInfo",[1060]]],"generic"]]`
	_, err := parseStream(strings.NewReader(line + "\n"))
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("error = %v, mau ErrLimit", err)
	}
}

// TestParseStreamTextWinsOverErrorInfo memastikan penanda error di baris
// berikutnya tidak membatalkan jawaban yang sudah lengkap.
func TestParseStreamTextWinsOverErrorInfo(t *testing.T) {
	line := buildStreamLine(t, streamInner([]string{"Jawaban lengkap."}))
	body := line + "\n" + `[["wrb.fr",null,null,null,null,[["BardErrorInfo",[1097]]],"generic"]]` + "\n"
	res, err := parseStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "Jawaban lengkap." {
		t.Fatalf("teks = %q", res.Text)
	}
}

func TestBuildChatRequestShape(t *testing.T) {
	raw := buildChatRequest("halo dunia", "id-ID")

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
	if len(req) != 3 {
		t.Fatalf("panjang payload = %d, mau 3", len(req))
	}
	prompt, _ := req[0].([]any)
	if len(prompt) != 1 || prompt[0] != "halo dunia" {
		t.Fatalf("slot 0 = %v", req[0])
	}
	lang, _ := req[1].([]any)
	if len(lang) != 1 || lang[0] != "id-ID" {
		t.Fatalf("slot 1 = %v", req[1])
	}
	resume, _ := req[2].([]any)
	if len(resume) != 0 {
		t.Fatalf("slot 2 harus kosong (tamu), dapat %v", req[2])
	}
}

func TestBuildChatRequestDefaultLang(t *testing.T) {
	raw := buildChatRequest("hai", "")
	var outer []any
	if err := json.Unmarshal([]byte(raw), &outer); err != nil {
		t.Fatalf("f.req: %v", err)
	}
	var req []any
	if err := json.Unmarshal([]byte(outer[1].(string)), &req); err != nil {
		t.Fatalf("payload: %v", err)
	}
	lang, _ := req[1].([]any)
	if len(lang) != 1 || lang[0] != defaultLang {
		t.Fatalf("bahasa default = %v", req[1])
	}
}
