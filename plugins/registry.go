package plugins

import (
	"context"

	"wabot-go/internal/store"
)

// Command mendeskripsikan satu perintah bot.
type Command struct {
	Name        string   // nama command tanpa prefix, misal "ping"
	Aliases     []string // nama alternatif, misal []string{"s"} untuk "sticker"
	Description string   // dipakai di menu bantuan
	Handler     func(ctx context.Context, mctx *store.MessageContext) error
}

// Registry menyimpan semua command yang terdaftar, key = nama command.
type Registry struct {
	commands map[string]Command
}

func NewRegistry() *Registry {
	return &Registry{commands: make(map[string]Command)}
}

// Register menambahkan satu command baru ke registry, sekaligus mendaftarkan
// tiap Aliases sebagai key tambahan yang menunjuk ke command yang sama.
// Command.All() memfilter alias secara otomatis (lihat All()), jadi menu.go
// tidak perlu tahu-menahu soal alias.
func (r *Registry) Register(cmd Command) {
	r.commands[cmd.Name] = cmd
	for _, alias := range cmd.Aliases {
		r.commands[alias] = cmd
	}
}

func (r *Registry) Get(name string) (Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

// All mengembalikan semua command terdaftar, dipakai oleh plugin menu.go.
// Hanya entry dengan key == cmd.Name yang dikembalikan (entry alias, key-nya
// beda dari cmd.Name, difilter) supaya command dengan alias tidak muncul
// dobel di menu.
func (r *Registry) All() map[string]Command {
	out := make(map[string]Command, len(r.commands))
	for key, cmd := range r.commands {
		if key == cmd.Name {
			out[key] = cmd
		}
	}
	return out
}

// --- Self-registration machinery ---

// registrars menampung semua fungsi pendaftaran yang didaftarkan oleh
// masing-masing file plugin lewat init(). Diisi otomatis sebelum main()
// jalan, karena Go menjalankan semua init() di package ini saat di-import.
var registrars []func(r *Registry)

// registerPlugin dipanggil dari init() tiap file plugin, BUKAN dipanggil
// manual dari luar. Ini pengganti baris manual yang dulu ada di RegisterAll.
func registerPlugin(fn func(r *Registry)) {
	registrars = append(registrars, fn)
}

// RegisterAll menjalankan semua fungsi yang sudah mendaftar lewat init().
// Tetap dipanggil sekali di main.go seperti sebelumnya, tapi isinya sekarang
// otomatis — tidak perlu diedit lagi setiap ada plugin baru.
func RegisterAll(r *Registry) {
	for _, register := range registrars {
		register(r)
	}
}
