package config

import "testing"

func TestResolveStickerAuthor(t *testing.T) {
	// 1. Env STICKER_AUTHOR text
	t.Setenv("STICKER_AUTHOR", "Mitra")
	if got := ResolveStickerAuthor("Budi"); got != "Mitra" {
		t.Errorf("got %q, want %q", got, "Mitra")
	}

	// 2. Env STICKER_AUTHOR = @user with pushname
	t.Setenv("STICKER_AUTHOR", "@user")
	if got := ResolveStickerAuthor("Budi"); got != "Budi" {
		t.Errorf("got %q, want %q", got, "Budi")
	}

	// 3. Env STICKER_AUTHOR = @user with empty pushname
	t.Setenv("STICKER_AUTHOR", "@user")
	if got := ResolveStickerAuthor("   "); got != DefaultStickerAuthor {
		t.Errorf("got %q, want %q", got, DefaultStickerAuthor)
	}

	// 4. Case insensitive @User
	t.Setenv("STICKER_AUTHOR", "@User")
	if got := ResolveStickerAuthor("Siti"); got != "Siti" {
		t.Errorf("got %q, want %q", got, "Siti")
	}

	// 5. Default
	t.Setenv("STICKER_AUTHOR", "")
	if got := ResolveStickerAuthor("Budi"); got != DefaultStickerAuthor {
		t.Errorf("got %q, want %q", got, DefaultStickerAuthor)
	}
}

func TestResolveStickerPackname(t *testing.T) {
	// 1. Explicit STICKER_PACKNAME
	t.Setenv("STICKER_PACKNAME", "MyCustomPack")
	if got := ResolveStickerPackname(); got != "MyCustomPack" {
		t.Errorf("got %q, want %q", got, "MyCustomPack")
	}

	// 2. STICKER_PACK_NAME alias
	t.Setenv("STICKER_PACKNAME", "")
	t.Setenv("STICKER_PACK_NAME", "AliasPack")
	if got := ResolveStickerPackname(); got != "AliasPack" {
		t.Errorf("got %q, want %q", got, "AliasPack")
	}

	// 3. Fallback to BOT_NAME
	t.Setenv("STICKER_PACKNAME", "")
	t.Setenv("STICKER_PACK_NAME", "")
	t.Setenv("BOT_NAME", "AwesomeBot")
	if got := ResolveStickerPackname(); got != "AwesomeBot" {
		t.Errorf("got %q, want %q", got, "AwesomeBot")
	}

	// 4. Fallback to DefaultBotName
	t.Setenv("BOT_NAME", "")
	if got := ResolveStickerPackname(); got != DefaultBotName {
		t.Errorf("got %q, want %q", got, DefaultBotName)
	}
}
