package logfmt

// Logger whatsmeow kustom: menggantikan waLog.Stdout agar log library
// (Client/Database) seragam dengan log bot — timestamp zona aktif timex
// (bukan jam server mentah), format blok, garis pembatas. Isi pesan asli
// library TIDAK diubah (tetap bisa dicari di Google/dilaporkan upstream).
//
// Memenuhi interface github.com/polymorfa/hypermeow/util/log.Logger (Warnf/Errorf/
// Infof/Debugf/Sub) sehingga bisa dipasang langsung ke NewClient/sqlstore.

import (
	"fmt"
	"strings"

	waLog "github.com/polymorfa/hypermeow/util/log"

	"wabot-go/internal/timex"
)

// waLevelToInt menyamakan filter level ala waLog ("" = semua lewat).
var waLevelToInt = map[string]int{
	"":      -1,
	"DEBUG": 0,
	"INFO":  1,
	"WARN":  2,
	"ERROR": 3,
}

type waBridgeLogger struct {
	mod string
	min int
}

// NewWALogger membuat Logger whatsmeow berformat blok bot. module mis.
// "Client"/"Database"; minLevel mis. "INFO"/"ERROR" (sama seperti Stdout).
func NewWALogger(module, minLevel string) waLog.Logger {
	return &waBridgeLogger{mod: module, min: waLevelToInt[strings.ToUpper(minLevel)]}
}

func (l *waBridgeLogger) outputf(level, msg string, args ...any) {
	if waLevelToInt[level] < l.min {
		return
	}
	text := fmt.Sprintf(msg, args...)
	fmt.Println(PaintCode(waLevelColor(level), fmt.Sprintf("[%s - %s]",
		timex.Now().Format("15:04"), strings.ToUpper(l.mod))))
	fmt.Printf("level : %s\ncontent :\n%s\n", level, text)
	fmt.Println(Divider())
}

// waLevelColor memetakan level ke warna header (konsisten palet logfmt:
// INFO cyan, WARN kuning, ERROR merah).
func waLevelColor(level string) string {
	switch level {
	case "INFO":
		return "36"
	case "WARN":
		return "33"
	case "ERROR":
		return "31"
	default:
		return ""
	}
}

func (l *waBridgeLogger) Errorf(msg string, args ...any) { l.outputf("ERROR", msg, args...) }
func (l *waBridgeLogger) Warnf(msg string, args ...any)  { l.outputf("WARN", msg, args...) }
func (l *waBridgeLogger) Infof(msg string, args ...any)  { l.outputf("INFO", msg, args...) }
func (l *waBridgeLogger) Debugf(msg string, args ...any) { l.outputf("DEBUG", msg, args...) }

// Sub membuat sub-logger "Modul/Area" dengan level yang sama — pola
// waLog.Stdout.Sub, supaya pemanggil library yang meminta Sub tetap
// mendapat format blok yang sama.
func (l *waBridgeLogger) Sub(module string) waLog.Logger {
	return &waBridgeLogger{mod: fmt.Sprintf("%s/%s", l.mod, module), min: l.min}
}
