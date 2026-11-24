package envload

import (
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	_ "github.com/joho/godotenv/autoload"
)

func init() {
	// Jika env belum dimuat (misal saat go test berjalan di subfolder),
	// telusuri direktori ke atas untuk menemukan root .env
	dir, err := os.Getwd()
	if err != nil {
		return
	}

	for i := 0; i < 5; i++ {
		target := filepath.Join(dir, ".env")
		if _, err := os.Stat(target); err == nil {
			_ = godotenv.Load(target)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}
