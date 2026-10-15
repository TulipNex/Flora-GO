package plugins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// buildMinimalPDF merakit PDF N halaman valid dengan offset xref yang
// dihitung (bukan hardcode) supaya pdfcpu mau memprosesnya.
func buildMinimalPDF(pages int) []byte {
	var buf bytes.Buffer
	var offsets []int
	buf.WriteString("%PDF-1.4\n")
	writeObj := func(n int, body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	kids := ""
	for i := 0; i < pages; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+i)
	}
	writeObj(1, "<</Type/Catalog/Pages 2 0 R>>")
	writeObj(2, fmt.Sprintf("<</Type/Pages/Kids[%s]/Count %d>>", kids, pages))
	for i := 0; i < pages; i++ {
		writeObj(3+i, "<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]>>")
	}
	xrefPos := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", pages+3)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF", pages+3, xrefPos)
	return buf.Bytes()
}

func pdfTestConf() *model.Configuration {
	return model.NewDefaultConfiguration()
}

func TestPDFOptimizeRoundTrip(t *testing.T) {
	in := buildMinimalPDF(2)
	var out bytes.Buffer
	if err := api.Optimize(context.Background(), bytes.NewReader(in), &out, pdfTestConf(), nil); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("%PDF")) {
		t.Fatal("output bukan PDF")
	}
}

func TestPDFTrimPages(t *testing.T) {
	in := buildMinimalPDF(3)
	var out bytes.Buffer
	if err := api.Trim(context.Background(), bytes.NewReader(in), &out, []string{"2"}, pdfTestConf()); err != nil {
		t.Fatalf("Trim: %v", err)
	}
	n, err := api.PageCount(context.Background(), bytes.NewReader(out.Bytes()), pdfTestConf())
	if err != nil {
		t.Fatalf("PageCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("halaman = %d, mau 1", n)
	}
}

func TestPDFWatermarkDesc(t *testing.T) {
	in := buildMinimalPDF(1)
	conf := pdfTestConf()
	wm, err := api.TextWatermark(context.Background(), "DRAF", "rot:45, op:.35, pos:c", true, false, 0, conf)
	if err != nil {
		t.Fatalf("desc watermark ditolak: %v", err)
	}
	var out bytes.Buffer
	if err := api.AddWatermarks(context.Background(), bytes.NewReader(in), &out, []string{"1"}, wm, conf); err != nil {
		t.Fatalf("AddWatermarks: %v", err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("%PDF")) {
		t.Fatal("output bukan PDF")
	}
}

func TestPDFProtectUnlockRoundTrip(t *testing.T) {
	in := buildMinimalPDF(1)
	pw := "rahasia123"
	encConf := pdfTestConf()
	encConf.OwnerPW = pw
	encConf.UserPW = pw
	encConf.UserPWNew = &pw
	encConf.OwnerPWNew = &pw
	var enc bytes.Buffer
	if err := api.Encrypt(context.Background(), bytes.NewReader(in), &enc, encConf); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	decConf := pdfTestConf()
	decConf.UserPW = pw
	decConf.OwnerPW = pw
	var dec bytes.Buffer
	if err := api.Decrypt(context.Background(), bytes.NewReader(enc.Bytes()), &dec, decConf); err != nil {
		t.Fatalf("Decrypt pw benar: %v", err)
	}
	if !bytes.HasPrefix(dec.Bytes(), []byte("%PDF")) {
		t.Fatal("hasil decrypt bukan PDF")
	}
	badConf := pdfTestConf()
	badConf.UserPW = "salah"
	var bad bytes.Buffer
	if err := api.Decrypt(context.Background(), bytes.NewReader(enc.Bytes()), &bad, badConf); err == nil {
		t.Fatal("decrypt pw salah harus error")
	}
}

func TestPDFImportImages(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{200, 30, 30, 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	var out bytes.Buffer
	if err := api.ImportImages(context.Background(), nil, &out,
		[]io.Reader{bytes.NewReader(pngBuf.Bytes())},
		api.DefaultImportConfig(), pdfTestConf()); err != nil {
		t.Fatalf("ImportImages: %v", err)
	}
	n, err := api.PageCount(context.Background(), bytes.NewReader(out.Bytes()), pdfTestConf())
	if err != nil {
		t.Fatalf("PageCount hasil: %v", err)
	}
	if n != 1 {
		t.Fatalf("halaman = %d, mau 1", n)
	}
}

// TestGenerateLockedSample membuat contoh PDF terkunci no-copy/no-print
// (user password kosong + owner password + PermissionsNone) ke
// wabot-tmp/locked-nocopynoprint.pdf untuk uji manual .unlock, sekaligus
// membuktikan Decrypt password-kosong tembus untuk kasus ini.
func TestGenerateLockedSample(t *testing.T) {
	in := buildMinimalPDF(2)
	owner := "owner123"
	encConf := pdfTestConf()
	encConf.OwnerPW = owner
	encConf.UserPWNew = new(string)
	encConf.OwnerPWNew = &owner
	encConf.Permissions = model.PermissionsNone
	var enc bytes.Buffer
	if err := api.Encrypt(context.Background(), bytes.NewReader(in), &enc, encConf); err != nil {
		t.Fatalf("Encrypt sample: %v", err)
	}
	if err := os.MkdirAll("../wabot-tmp", 0o755); err != nil {
		t.Fatalf("mkdir wabot-tmp: %v", err)
	}
	samplePath := "../wabot-tmp/locked-nocopynoprint.pdf"
	if err := os.WriteFile(samplePath, enc.Bytes(), 0o644); err != nil {
		t.Fatalf("tulis sample: %v", err)
	}
	t.Logf("sample tersimpan: %s (%d byte)", samplePath, enc.Len())

	decConf := pdfTestConf()
	var dec bytes.Buffer
	if err := api.Decrypt(context.Background(), bytes.NewReader(enc.Bytes()), &dec, decConf); err != nil {
		t.Fatalf("Decrypt password-kosong: %v", err)
	}
	if !bytes.HasPrefix(dec.Bytes(), []byte("%PDF")) {
		t.Fatal("hasil decrypt bukan PDF")
	}
}
