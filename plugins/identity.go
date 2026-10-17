package plugins

import (
	"context"
	"fmt"
	"strings"

	"wabot-go/internal/identity"
	"wabot-go/internal/store"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

func init() {
	registerPlugin(RegisterIdentity)
}

// identity.go — command diagnostik owner `.id` untuk melihat bagaimana bot
// menormalkan identitas (JID/LID/PN). Berguna saat ada laporan "bot tidak
// mengenali X": tampilkan JID mentah, alt, hasil resolve, dan kunci kanonik.
//
//   - .id             → identitas konteks sekarang (pengirim + chat).
//   - .id <nomor|@jid> → normalisasi input arbitrer (PN + LID bila diketahui).
//
// Disembunyikan dari .menu (Hidden) — alat owner, bukan fitur umum.

func RegisterIdentity(r *Registry) {
	r.Register(Command{
		Name:        "id",
		Description: "Lihat normalisasi identitas (JID/LID/PN) — owner",
		Category:    CategoryOwner,
		Hidden:      true,
		Handler:     handleID,
	})
}

func handleID(ctx context.Context, mctx *store.MessageContext) error {
	if msg := requireOwner(mctx); msg != "" {
		return store.ReplyText(ctx, mctx, msg)
	}
	if len(mctx.Args) > 0 {
		jid, err := parseIDArg(strings.Join(mctx.Args, " "))
		if err != nil {
			return store.ReplyText(ctx, mctx, "Tidak bisa membaca JID/nomor: "+err.Error())
		}
		return store.ReplyText(ctx, mctx, idReport(ctx, mctx.Client, "Input", jid, types.JID{}))
	}
	var alt types.JID
	if mctx.RawEvt != nil {
		alt = mctx.RawEvt.Info.SenderAlt
	}
	report := idReport(ctx, mctx.Client, "Sender", mctx.Sender, alt)
	kind := "pribadi"
	if mctx.IsGroup() {
		kind = "grup"
	}
	report += fmt.Sprintf("\nChat    : %s (%s)", dash(mctx.Chat.String()), kind)
	return store.ReplyText(ctx, mctx, report)
}

// idReport menyusun ringkasan normalisasi untuk satu JID (+ alt opsional).
func idReport(ctx context.Context, client *whatsmeow.Client, label string, jid, alt types.JID) string {
	pn, pnOK := identity.ResolvePN(ctx, client, jid, alt)
	lid, lidOK := identity.ResolveLID(ctx, client, jid, alt)
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Identitas %s*\n", label)
	fmt.Fprintf(&sb, "JID     : %s\n", dash(jid.String()))
	fmt.Fprintf(&sb, "Alt     : %s\n", dash(alt.String()))
	fmt.Fprintf(&sb, "Bare    : %s\n", dash(identity.UserPart(jid)))
	fmt.Fprintf(&sb, "PN      : %s\n", idVal(pn, pnOK))
	fmt.Fprintf(&sb, "LID     : %s\n", idVal(lid, lidOK))
	fmt.Fprintf(&sb, "Kanonik : %s\n", dash(identity.CanonicalUser(ctx, client, jid, alt)))
	fmt.Fprintf(&sb, "Bot?    : %s", yaTidak(identity.IsBot(client, jid)))
	return sb.String()
}

// parseIDArg menerima "user@server" (parse JID) atau nomor HP polos.
func parseIDArg(raw string) (types.JID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return types.JID{}, fmt.Errorf("kosong")
	}
	if strings.Contains(raw, "@") {
		return types.ParseJID(raw)
	}
	return ParsePhoneToJID(raw)
}

func idVal(j types.JID, ok bool) string {
	if !ok {
		return "-"
	}
	return j.String()
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func yaTidak(b bool) string {
	if b {
		return "ya"
	}
	return "tidak"
}
