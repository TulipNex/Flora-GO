package plugins

import (
	"fmt"
	"strings"
	"time"

	"wabot-go/internal/textx"
)

const (
	execTimeout  = 120 * time.Second
	evalTimeout  = 30 * time.Second
	maxExecChars = 3500
)

func appendExecLine(out, line string) string {
	if strings.TrimSpace(out) == "" {
		return line
	}
	return out + "\n" + line
}

func truncateRunesExec(text string) string {
	out, total, cut := textx.TruncateRunes(text, maxExecChars, false)
	if !cut {
		return out
	}
	return out + fmt.Sprintf("\n...(dipotong, total %d karakter)", total)
}

type evalTimeoutSentinel struct{}

func (evalTimeoutSentinel) Error() string { return "eval timeout" }
