package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"wabot-go/internal/store"

	"github.com/robertkrimen/otto"
)

// prettyJSON merangkai nilai jadi JSON indent 2 spasi untuk balasan eval.
func prettyJSON(v interface{}) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func init() {
	registerPlugin(RegisterOwnerExec)
}

// Batasan pengaman eksekusi owner (shell & eval). Tanpa ini, satu perintah
// macet atau output raksasa bisa menggantung bot / meledakkan pesan WA.
const (
	execTimeout  = 120 * time.Second // shell: bunuh otomatis kalau lewat (fetch web lambat butuh ruang)
	evalTimeout  = 30 * time.Second  // eval JS: interrupt VM kalau lewat
	maxExecChars = 3500              // potong output balasan (per rune)
)

// RegisterOwnerExec mendaftarkan dua command eksekusi khusus owner:
// "$" (shell server, mis. `$ ls -la data`) dan "\" (eval JavaScript,
// mis. `\ 2+2*3`). Keduanya dirute KHUSUS di handler (bukan prefix ".")
// supaya tetap satu karakter. Kategori Owner = tersembunyi dari non-owner.
func RegisterOwnerExec(r *Registry) {
	r.Register(Command{
		Name:        "$",
		Description: "Eksekusi perintah shell di server (owner)",
		Category:    CategoryOwner,
		Handler:     handleShell,
	})
	r.Register(Command{
		Name:        "\\",
		Description: "Eval JavaScript di dalam bot (owner)",
		Category:    CategoryOwner,
		Handler:     handleEvalJS,
	})
}

func handleShell(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	script := strings.TrimSpace(mctx.RawArgs)
	if script == "" {
		return store.ReplyText(ctx, mctx, "Contoh: $ ls -la data")
	}

	// Async: eksekusi jalan di goroutine sendiri dan balasan dikirim saat
	// selesai. Handler langsung return supaya event loop whatsmeow tidak
	// terblokir perintah lama (fetch web dsb) — pelajaran dari warning
	// "Node handling took Xs" di jalur AI. Balasan tetap satu pesan berisi
	// output (atau status timeout/error).
	go func() {
		bg := context.Background()
		// Timeout via context: proses dibunuh otomatis kalau macet (top,
		// cat tanpa argumen, dsb) — beda dari versi Hisoka yang bisa
		// gantung selamanya.
		timeoutCtx, cancel := context.WithTimeout(bg, execTimeout)
		defer cancel()

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(timeoutCtx, "cmd", "/C", script)
		} else {
			cmd = exec.CommandContext(timeoutCtx, "sh", "-c", script)
		}
		// Kerja di folder bot supaya path relatif (data/, .env) predictible.
		cmd.Dir = "."

		// Stdout+stderr digabung supaya error perintah tidak hilang sebelah
		// (versi Hisoka hanya membalas err, output yang gagal sebagian lenyap).
		out, err := cmd.CombinedOutput()
		text := strings.TrimRight(string(out), "\n")
		if err != nil {
			if timeoutCtx.Err() == context.DeadlineExceeded {
				text = appendExecLine(text, fmt.Sprintf("[dihentikan: melebihi %s]", execTimeout))
			} else {
				text = appendExecLine(text, fmt.Sprintf("[exit error: %v]", err))
			}
		}
		if strings.TrimSpace(text) == "" {
			text = "(tidak ada output)"
		}
		if rerr := store.ReplyText(bg, mctx, truncateRunesExec(text)); rerr != nil {
			fmt.Printf("❌ Gagal balas hasil shell: %v\n", rerr)
		}
	}()
	return nil
}

// appendExecLine menempelkan baris status ke output (atau jadi isinya kalau
// output kosong).
func appendExecLine(out, line string) string {
	if strings.TrimSpace(out) == "" {
		return line
	}
	return out + "\n" + line
}

// truncateRunesExec memotong teks balasan ke maxExecChars per rune + penanda.
func truncateRunesExec(text string) string {
	runes := []rune(text)
	if len(runes) <= maxExecChars {
		return text
	}
	return string(runes[:maxExecChars]) + fmt.Sprintf("\n...(dipotong, total %d karakter)", len(runes))
}

// evalTimeoutSentinel adalah penanda khusus panic interrupt otto supaya bisa
// dibedakan dari panic kode user. Dipakai via recover di handleEvalJS.
// Menerapkan error supaya bisa di-assert dari nilai error vm.Run.
type evalTimeoutSentinel struct{}

func (evalTimeoutSentinel) Error() string { return "eval timeout" }

func handleEvalJS(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	code := strings.TrimSpace(mctx.RawArgs)
	if code == "" {
		return store.ReplyText(ctx, mctx, `Contoh: \ 2+2*3`)
	}

	// Async seperti shell: VM jalan di goroutine sendiri, balasan dikirim
	// saat selesai, event loop tidak terblokir.
	go func() {
		bg := context.Background()
		vm := otto.New()
		// WAJIB diinisialisasi: default-nya nil, dan kirim ke channel nil
		// menggantung selamanya (timer jalan tapi VM tidak pernah berhenti).
		// Buffer 1 supaya pengirim tidak blok (lihat contoh di otto.go).
		vm.Interrupt = make(chan func(), 1)

		// HANYA objek MSG yang disuntikkan (prinsip hak akses minimum) —
		// beda dari Hisoka yang menyuntikkan seluruh Conn (bisa
		// kirim/tendang sembarang). Isinya info baca-saja tentang pesan
		// pemicu.
		_ = vm.Set("MSG", map[string]interface{}{
			"text":     extractEvalText(mctx),
			"sender":   mctx.Sender.String(),
			"chat":     mctx.Chat.String(),
			"isGroup":  mctx.IsGroup(),
			"pushName": mctx.RawEvt.Info.PushName,
		})

		// Interrupt timeout: otto tidak punya timeout bawaan, jadi dipasang
		// manual — tanpa ini `while(true){}` menggantung bot selamanya.
		// Panic sentinel ditangkap recover di bawah, bukan diteruskan.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-done:
			case <-time.After(evalTimeout):
				vm.Interrupt <- func() { panic(evalTimeoutSentinel{}) }
			}
		}()

		var result string
		func() {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(evalTimeoutSentinel); ok {
						result = fmt.Sprintf("[dihentikan: melebihi %s]", evalTimeout)
					} else {
						result = fmt.Sprintf("[panic: %v]", r)
					}
				}
			}()
			result = runEvalVM(vm, code)
		}()

		if strings.TrimSpace(result) == "" {
			result = "(tidak ada output)"
		}
		if rerr := store.ReplyText(bg, mctx, truncateRunesExec(result)); rerr != nil {
			fmt.Printf("❌ Gagal balas hasil eval: %v\n", rerr)
		}
	}()
	return nil
}

// runEvalVM menjalankan kode di VM dan merangkai hasilnya: object/array
// di-JSON-kan rapi (indent 2, seperti Hisoka), primitif jadi string.
func runEvalVM(vm *otto.Otto, code string) string {
	v, err := vm.Run(code)
	if err != nil {
		// Panic sentinel interrupt diteruskan ke recover pemanggil.
		if _, ok := err.(evalTimeoutSentinel); ok {
			panic(err)
		}
		return err.Error()
	}
	if v.IsObject() {
		if exported, experr := v.Export(); experr == nil {
			if pretty, perr := prettyJSON(exported); perr == nil {
				return pretty
			}
		}
		// Fallback: stringify dari dalam VM seperti Hisoka.
		if sv, serr := vm.Run("JSON.stringify(" + code + ")"); serr == nil {
			return sv.String()
		}
	}
	return v.String()
}

// extractEvalText mengambil teks pesan pemicu untuk MSG.text.
func extractEvalText(mctx *store.MessageContext) string {
	if mctx.RawEvt == nil {
		return ""
	}
	msg := store.UnwrapMessage(mctx.RawEvt.Message)
	if msg == nil {
		return ""
	}
	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}
