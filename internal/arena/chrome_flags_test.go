package arena

import (
	"testing"
	"time"
)

// TestHeadedFlags memastikan Chrome di layar virtual mematikan throttling
// jendela ter-occlude — penyebab halaman arena.ai berhenti di
// readyState=loading di panel (Xvfb tanpa window manager).
func TestHeadedFlags(t *testing.T) {
	t.Setenv("ARENA_HEADED_GPU", "")
	args := headedFlags()
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-features=CalculateNativeWinOcclusion",
		"--disable-gpu",
	} {
		if !has(want) {
			t.Errorf("flag %s tidak ada di %v", want, args)
		}
	}

	t.Setenv("ARENA_HEADED_GPU", "1")
	if has2 := headedFlags(); func() bool {
		for _, a := range has2 {
			if a == "--disable-gpu" {
				return true
			}
		}
		return false
	}() {
		t.Error("ARENA_HEADED_GPU=1 harus mempertahankan GPU path (tanpa --disable-gpu)")
	}
}

// TestReadyTimeout: mode tampilan diberi waktu lebih panjang daripada headless,
// dan env ARENA_READY_SECONDS menimpanya.
func TestReadyTimeout(t *testing.T) {
	t.Setenv("ARENA_READY_SECONDS", "")
	if got := (&browser{headed: true}).readyTimeout(); got != 45*time.Second {
		t.Errorf("headed readyTimeout = %v, mau 45s", got)
	}
	if got := (&browser{}).readyTimeout(); got != chromeReadyTimeout {
		t.Errorf("headless readyTimeout = %v, mau %v", got, chromeReadyTimeout)
	}
	t.Setenv("ARENA_READY_SECONDS", "90")
	if got := (&browser{headed: true}).readyTimeout(); got != 90*time.Second {
		t.Errorf("env tidak dipakai: %v", got)
	}
}

// TestRemoveHeadlessFlags memastikan flag headless (termasuk --disable-gpu
// bawaan) dibuang sebelum headedFlags menambahkannya kembali.
func TestRemoveHeadlessFlags(t *testing.T) {
	in := []string{"--headless=new", "--disable-gpu", "--no-sandbox"}
	out := removeHeadlessFlags(in)
	if len(out) != 1 || out[0] != "--no-sandbox" {
		t.Errorf("hasil = %v", out)
	}
}
