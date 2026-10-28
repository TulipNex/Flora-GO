package plugins

import (
	"fmt"

	"wabot-go/internal/external"
)

func init() {
	registerPlugin(RegisterShutterstock)
}

// RegisterShutterstock mendaftarkan command `.shutterstock` (alias `.shutter`
// dan `.ss`): unduh gambar HD (±1600px lebar) dari sebuah ID aset/URL
// Shutterstock lewat perantara steptodown.com. Menerima URL halaman maupun ID
// aset mentah; gambar yang dikembalikan BERSIH — tanpa watermark dan tanpa bar
// footer. Kategori Unduh, boleh dipakai semua user.
func RegisterShutterstock(r *Registry) {
	registerSteptodown(r, steptodownCmd{
		name:    "shutterstock",
		aliases: []string{"shutter", "ss"},
		tool:    "shutterstock-downloader",
		label:   "Shutterstock",
		hint:    "<id aset/url>",
		example: "2583067397",
		key:     "ID",
		resolve: func(raw string) (string, error) {
			id := external.ShutterstockExtractID(raw)
			if id == "" {
				return "", fmt.Errorf("ID aset tidak valid. Contoh: 2583067397")
			}
			return id, nil
		},
	})
}
