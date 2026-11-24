package msgbuilder

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateVerificationMetadata(t *testing.T) {
	meta := GenerateVerificationMetadata("4.7")
	if meta == nil || len(meta.Proofs) != 1 {
		t.Fatalf("proofs tidak valid: %+v", meta)
	}
	proof := meta.Proofs[0]
	if len(proof.Signature) != 64 {
		t.Fatalf("signature panjang %d, mau 64", len(proof.Signature))
	}
	if !strings.HasPrefix(string(proof.Signature), "NIXEL.MessageBuilderV4.7-VerificationSignature.Metadata") {
		t.Fatalf("prefix signature salah: %s", string(proof.Signature))
	}
	if len(proof.CertificateChain) != 2 {
		t.Fatalf("certificate chain = %d, mau 2", len(proof.CertificateChain))
	}
	if len(proof.CertificateChain[0]) != 684 {
		t.Fatalf("cert 1 panjang %d, mau 684", len(proof.CertificateChain[0]))
	}
	if len(proof.CertificateChain[1]) != 892 {
		t.Fatalf("cert 2 panjang %d, mau 892", len(proof.CertificateChain[1]))
	}
	if !strings.HasPrefix(string(proof.CertificateChain[0]), "NIXEL.MessageBuilderV4.7-CertificateChain.Metadata") {
		t.Fatalf("prefix cert 1 salah: %s", string(proof.CertificateChain[0]))
	}
}

func TestTokenizeCode(t *testing.T) {
	code := `// komentar
func main() {
	var x = 42
	println("hello")
}`
	protoBlocks, unified := TokenizeCode("go", code)
	if len(protoBlocks) == 0 || len(unified) == 0 {
		t.Fatalf("token kosong")
	}

	foundComment := false
	foundKeyword := false
	foundNumber := false
	foundStr := false

	for _, u := range unified {
		switch u["type"] {
		case "COMMENT":
			foundComment = true
		case "KEYWORD":
			foundKeyword = true
		case "NUMBER":
			foundNumber = true
		case "STR":
			foundStr = true
		}
	}

	if !foundComment || !foundKeyword || !foundNumber || !foundStr {
		t.Fatalf("tipe token tidak lengkap: comment=%v, kw=%v, num=%v, str=%v; tokens: %+v",
			foundComment, foundKeyword, foundNumber, foundStr, unified)
	}
}

func TestAIRichBuilderFull(t *testing.T) {
	b := NewAIRich().
		SetTitle("Test Disclaimer").
		SetFooter("Footer Text").
		AddHeading("Judul Besar").
		AddMarkdown("Teks *tebal*").
		AddCode("python", "def hello():\n    return 42").
		AddTable([]string{"Nama", "Umur"}, [][]string{{"Alice", "25"}, {"Bob", "30"}}).
		AddSuggest("Prompt 1", "Prompt 2").
		AddTip("Ini tip penting").
		AddMetadata("2026-11-18 21:00").
		AddImagine("https://contoh.com/thumb.jpg", "https://contoh.com/full.jpg", "IMAGE", "READY", "Selesai").
		AddSource("Google", "Mesin Pencari", "https://google.com", "https://google.com/icon.png")

	msg := b.Build()
	if msg == nil {
		t.Fatal("msg nil")
	}

	rich := msg.GetBotForwardedMessage().GetMessage().GetRichResponseMessage()
	if rich == nil {
		t.Fatal("RichResponseMessage nil")
	}

	dataBytes := rich.GetUnifiedResponse().GetData()
	var payload map[string]any
	if err := json.Unmarshal(dataBytes, &payload); err != nil {
		t.Fatalf("gagal unmarshal unifiedResponse data: %v", err)
	}

	sections, ok := payload["sections"].([]any)
	if !ok || len(sections) < 9 {
		t.Fatalf("sections tidak sesuai, dapat %d", len(sections))
	}

	submessages := rich.GetSubmessages()
	if len(submessages) < 6 {
		t.Fatalf("submessages tidak lengkap, dapat %d", len(submessages))
	}

	editMsg := b.BuildEdit("TARGET_MSG_123")
	protoMsg := editMsg.GetBotForwardedMessage().GetMessage().GetProtocolMessage()
	if protoMsg == nil {
		t.Fatal("ProtocolMessage nil")
	}
	if protoMsg.GetKey().GetID() != "TARGET_MSG_123" {
		t.Fatalf("target id salah: %s", protoMsg.GetKey().GetID())
	}
	if int32(protoMsg.GetType()) != 14 {
		t.Fatalf("type bukan 14 (MESSAGE_EDIT): %d", protoMsg.GetType())
	}
}
