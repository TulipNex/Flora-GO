package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"

	"github.com/robertkrimen/otto"
)

func init() {
	registerPlugin(RegisterEvalJS)
}

func RegisterEvalJS(r *Registry) {
	r.Register(Command{
		Name:        "\\",
		Description: "Eval JavaScript di dalam bot (owner)",
		Category:    CategoryOwner,
		Handler:     handleEvalJS,
	})
}

func prettyJSON(v interface{}) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func handleEvalJS(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	code := strings.TrimSpace(mctx.RawArgs)
	if code == "" {
		return store.ReplyText(ctx, mctx, `Contoh: \ 2+2*3`)
	}

	go func() {
		bg := context.Background()
		vm := otto.New()
		vm.Interrupt = make(chan func(), 1)

		_ = vm.Set("MSG", map[string]interface{}{
			"text":     extractEvalText(mctx),
			"sender":   mctx.Sender.String(),
			"chat":     mctx.Chat.String(),
			"isGroup":  mctx.IsGroup(),
			"pushName": mctx.RawEvt.Info.PushName,
		})

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
			logfmt.Error("Gagal balas hasil eval: %v\n", rerr)
		}
	}()
	return nil
}

func runEvalVM(vm *otto.Otto, code string) string {
	v, err := vm.Run(code)
	if err != nil {
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
		if sv, serr := vm.Run("JSON.stringify(" + code + ")"); serr == nil {
			return sv.String()
		}
	}
	return v.String()
}

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
