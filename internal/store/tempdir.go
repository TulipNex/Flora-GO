package store

import (
	"os"
	"path/filepath"
)

// botTempDirName adalah folder temp milik bot di dalam working directory
// (BUKAN os.TempDir()/TMPDIR sistem).
//
// KASUS NYATA (Sep 2026, panel Pterodactyl): /tmp adalah tmpfs 100MB,
// sementara /home/container lega 196GB. Go os.MkdirTemp("", ...) jatuh ke
// /tmp bila $TMPDIR tak di-set — video >~50MB (butuh ~2x selama merge/
// transcode in+out) DIJAMIN Errno 28 No space left. Semua pemakaian temp
// (yt-dlp, transcode, thumb, webp, stiker) WAJIB lewat helper ini.
const botTempDirName = "wabot-tmp"

// BotTempDir mengembalikan folder temp bot (<workdir>/wabot-tmp),
// membuatnya bila belum ada. Gagal buat = error (pemanggil tidak boleh
// diam-diam jatuh kembali ke /tmp — itu mengembalikan bug plafon).
func BotTempDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cwd, botTempDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// MustBotTempDir sama seperti BotTempDir tapi fallback ke os.TempDir()
// bila workdir tak bisa dipakai — hanya untuk konteks yang tak bisa
// mengembalikan error (lebih baik lambat/terbatas daripada crash).
func MustBotTempDir() string {
	if dir, err := BotTempDir(); err == nil {
		return dir
	}
	return os.TempDir()
}
