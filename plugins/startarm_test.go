package plugins

import (
	"testing"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

func armTestKey(user string) string {
	return startArmKey(
		types.JID{User: user, Server: types.DefaultUserServer},
		types.JID{User: user, Server: types.DefaultUserServer},
	)
}

// startArmKey harus menormalkan device supaya pesan dari device lain tetap
// satu jendela (selaras pdfStageKey).
func TestStartArmKeyNormalizesDevice(t *testing.T) {
	base := types.JID{User: "628111", Server: types.DefaultUserServer}
	chat := types.JID{User: "628111", Server: types.DefaultUserServer}
	withDevice := base
	withDevice.Device = 7

	if a, b := startArmKey(base, chat), startArmKey(withDevice, chat); a != b {
		t.Fatalf("kunci beda untuk device berbeda: %q vs %q", a, b)
	}

	otherChat := types.JID{User: "628222", Server: types.DefaultUserServer}
	if a, b := startArmKey(base, chat), startArmKey(base, otherChat); a == b {
		t.Fatal("chat berbeda harus punya kunci berbeda")
	}
}

// Jendela sekali pakai: take pertama benar, take kedua salah.
func TestStartArmTakeOnce(t *testing.T) {
	key := armTestKey("628100")
	t.Cleanup(func() { startArmClear(key) })

	startArmSet(key, startArmPDF)
	if !startArmTake(key, startArmPDF) {
		t.Fatal("take pertama harus true")
	}
	if startArmTake(key, startArmPDF) {
		t.Fatal("take kedua harus false (sekali pakai)")
	}
}

// Tool lain tidak boleh memakai jendela, dan tidak boleh mengonsumsinya.
func TestStartArmTakeWrongTool(t *testing.T) {
	key := armTestKey("628101")
	t.Cleanup(func() { startArmClear(key) })

	startArmSet(key, startArmPDF)
	if startArmTake(key, startArmJPG) {
		t.Fatal("jendela pdf tidak boleh dipakai tool jpg")
	}
	if !startArmTake(key, startArmPDF) {
		t.Fatal("jendela pdf harus utuh setelah take tool salah")
	}
}

// Setelah TTL lewat, jendela hilang diam-diam (tanpa notifikasi = tak ada
// efek samping apa pun di sini).
func TestStartArmExpires(t *testing.T) {
	key := armTestKey("628102")
	t.Cleanup(func() { startArmClear(key) })

	startArmSetFor(key, startArmPDF, 20*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	if startArmTake(key, startArmPDF) {
		t.Fatal("jendela harus kedaluwarsa setelah TTL")
	}
}

// Arm ulang menyegarkan jendela: timer lama tidak boleh membunuh entri baru.
func TestStartArmSetRefreshes(t *testing.T) {
	key := armTestKey("628103")
	t.Cleanup(func() { startArmClear(key) })

	startArmSetFor(key, startArmPDF, 40*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	startArmSetFor(key, startArmPDF, 200*time.Millisecond)
	time.Sleep(60 * time.Millisecond) // total 80ms > TTL pertama
	if !startArmTake(key, startArmPDF) {
		t.Fatal("arm ulang harus menyegarkan jendela")
	}
}

// Sesi yang sudah ada selalu boleh "mulai" (pemanggil menjawab "masih aktif")
// dan jendelanya ikut dibersihkan; tanpa sesi, jendela wajib hidup.
func TestStartArmAllows(t *testing.T) {
	key := armTestKey("628104")
	t.Cleanup(func() { startArmClear(key) })

	if startArmAllows(key, startArmPDF, false) {
		t.Fatal("tanpa sesi + tanpa jendela harus ditolak")
	}

	startArmSet(key, startArmPDF)
	if !startArmAllows(key, startArmPDF, false) {
		t.Fatal("tanpa sesi + jendela hidup harus diterima")
	}
	if startArmTake(key, startArmPDF) {
		t.Fatal("jendela harus terpakai setelah allows")
	}

	startArmSet(key, startArmPDF)
	if !startArmAllows(key, startArmPDF, true) {
		t.Fatal("sesi yang sudah ada selalu boleh mulai")
	}
	if startArmTake(key, startArmPDF) {
		t.Fatal("jendela harus dibersihkan saat sesi sudah ada")
	}
}

// Tap tombol pada menu basi: tidak boleh membuka sesi, tapi jendela
// disegarkan supaya pemanggil bisa mengirim menu baru.
func TestStartArmTakeOrRearm(t *testing.T) {
	key := armTestKey("628105")
	t.Cleanup(func() { startArmClear(key) })

	if startArmTakeOrRearm(key, startArmPDF) {
		t.Fatal("tanpa jendela harus false")
	}
	// rearm meninggalkan jendela hidup → panggilan berikut membuka sesi
	if !startArmTakeOrRearm(key, startArmPDF) {
		t.Fatal("jendela hasil rearm harus bisa dipakai")
	}
	if startArmTake(key, startArmPDF) {
		t.Fatal("jendela harus terpakai")
	}
}

// Kata polos "mulai" tanpa jendela tidak boleh ditelan staging PDF maupun JPG
// (client nil aman: jalur ini harus return false sebelum menyentuh jaringan).
func TestStartArmGatesPlainMulai(t *testing.T) {
	sender := types.JID{User: "628106", Server: types.DefaultUserServer}
	chat := types.JID{User: "628106", Server: types.DefaultUserServer}
	key := startArmKey(sender, chat)
	t.Cleanup(func() { startArmClear(key) })
	startArmClear(key)

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            "TESTMULAI",
		},
		Message: &waE2E.Message{Conversation: proto.String("mulai")},
	}
	if CheckPDFStaging(nil, evt) {
		t.Fatal("CheckPDFStaging harus false tanpa jendela .pdfmerge")
	}
	if CheckJpgStaging(nil, evt) {
		t.Fatal("CheckJpgStaging harus false tanpa jendela .jpgpdf")
	}

	// Jendela milik tool lain juga tidak boleh dipakai.
	startArmSet(key, startArmJPG)
	if CheckPDFStaging(nil, evt) {
		t.Fatal("jendela jpg tidak boleh membuka sesi PDF")
	}
	if !startArmTake(key, startArmJPG) {
		t.Fatal("jendela jpg harus utuh setelah ditolak pdfmerge")
	}
}
