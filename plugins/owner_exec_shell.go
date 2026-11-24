package plugins

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"wabot-go/internal/logfmt"
	"wabot-go/internal/store"
)

func init() {
	registerPlugin(RegisterShell)
}

func RegisterShell(r *Registry) {
	r.Register(Command{
		Name:        "$",
		Description: "Eksekusi perintah shell di server (owner)",
		Category:    CategoryOwner,
		Handler:     handleShell,
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

	go func() {
		bg := context.Background()
		timeoutCtx, cancel := context.WithTimeout(bg, execTimeout)
		defer cancel()

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(timeoutCtx, "cmd", "/C", script)
		} else {
			cmd = exec.CommandContext(timeoutCtx, "sh", "-c", script)
		}
		cmd.Dir = "."

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
			logfmt.Error("Gagal balas hasil shell: %v\n", rerr)
		}
	}()
	return nil
}
