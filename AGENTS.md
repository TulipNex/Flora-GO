# AGENTS.md — wabot-go (Flora GO)

WhatsApp bot in Go (`hypermeow` fork of `whatsmeow`, pure-Go `modernc.org/sqlite`, no cgo). Module `wabot-go`, Go 1.26+. Entry: `main.go`. No CI, no lint/typecheck config, no `opencode.json`.

## Run / verify

- `go run .` — but there is **no dotenv loader** (only `os.Getenv`, no godotenv/viper). The `.env` file is never auto-read; export vars in the shell first (see `scripts/test-env.ps1` pattern: `$env:OWNER_NUMBERS=...` then `go run .`).
- `OWNER_NUMBERS` (comma-separated digits) is mandatory — empty panics at startup. `LOGIN_METHOD` must be `qr` or `pairing`, else panic. `PHONE_NUMBER` required for pairing.
- `go build ./...` · `go vet ./...` (clean) · `go test ./...`
- Only `plugins` has tests: `go test ./plugins/ -v`; single test: `go test ./plugins/ -run TestNoCommandAliasCollision -v`
- `ffmpeg` must be in PATH wherever the bot runs (stickers, thumbnails, transcode fail otherwise).

## Add a command (plugins/)

Self-registration — never edit `registry.go`. New file in `plugins/` with `init() { registerPlugin(RegisterX) }`; `Category` must be a `CategoryXxx` const (free strings spawn ghost menu groups); `Hidden: true` + `CategoryHidden` to hide from `.menu`. Aliases share the command map and **silently overwrite** — `go test ./plugins/` catches collisions. `.tt` belongs to `tiktok` (regression-tested; tictactoe uses `.ttt`/`.tc`).

## Add an AI tool (internal/ai/)

Same pattern, different registry: put pure tools in `internal/ai/tools/native/`, WhatsApp-access tools in `internal/ai/tools/bridge/`, register via `ai.RegisterTool(...)` in `init()` — then add the **side-effect import in `main.go`**, or the tool silently never registers.

## Gotchas that bite

- Temp files: always `store.BotTempDir()` / `MustBotTempDir()` (`<workdir>/wabot-tmp`). Never `os.MkdirTemp`/`os.TempDir()` — panel `/tmp` is a 100 MB tmpfs, videos >~50 MB die with Errno 28.
- `data/` = session (`session.db`) + SQLite state (group/ai/facts/settings/finance/crm/absensi). Never delete, never commit (gitignored with `.env`, `wabot-go-linux`, `wabot-tmp/`). `scripts/push.ps1` warns on sensitive paths — heed it. `scripts/test-env.ps1` holds real secrets, stays uncommitted.
- Schema changes: each bot DB (`group/ai/finance/crm/absensi`) opens via `store.openSQLite` and applies schema through `store.applySchema` (`internal/store/schema.go`), which tracks `PRAGMA user_version` per file. When you add/alter a column, bump the matching `*SchemaVersion` const there and append the `ALTER` to the caller's slice. `ai.db` is one file shared by ai/facts/settings/summaries — the version const is per-file (`aiSchemaVersion`), not per table.
- Identity (owner name, bot name) is **compile-time** in `internal/config/config.go` — changing it needs rebuild + redeploy. `BotVersion` is a `var` injected at build time from the `VERSION` file via `-ldflags -X wabot-go/internal/config.BotVersion=...` (fallback = default in config.go); `scripts/deploy.ps1` auto-bumps it via `scripts/version-bump.ps1` to CalVer `v<bulan>.<tanggal>.<urut>` (tanggal deploy waktu lokal + urutan deploy hari itu), but only when code/tooling files changed since the last deploy (docs/data only = no bump; snapshot in gitignored `.last-deploy-state`). Provider/model choice is **runtime** in SQLite (`.agent` commands, no redeploy).
- WhatsApp identity is dual-format (LID vs phone number): never compare JIDs by raw string. Use `internal/identity` (the single source of truth: `Normalize`, `SameUser`, `Candidate.Matches`, `ResolvePN`/`ResolveLID`, `Keys`, `CanonicalUser`, `IsBot`) plus `store.ParticipantMatchesJID` / `IsSenderAdmin` / `IsBotAdmin` / `store.IsBotJID`. Owner/diagnostic: `.id` (owner) prints how the bot normalizes a sender/JID. A 1:1 DM can arrive as `@lid`, so detect private chat with `mctx.IsGroup()`, NOT `Chat.Server == "s.whatsapp.net"`.
- DB backups (`.backup` in `plugins/backup.go`): snapshot via `store.SnapshotDB` (`internal/store/snapshot.go`, SQLite `VACUUM INTO`) — never copy `data/*.db` directly, WAL make that inconsistent. Auto-backup schedule lives in `bot_settings` (`ownerdb.go`), result DM'd to `config.OwnerNumbers`; temp zips under `MustBotTempDir()` are always deleted after send.
- Keuangan bulanan (`plugins/finance_monthly.go` + `internal/finance/`): `data/finance.db` hanya menyimpan transaksi **bulan berjalan**. Saat pergantian bulan, sweep menutup periode lampau: rakit laporan Excel → arsip di `data/arsip-keuangan/<YYYY>/` → kirim DM owner → bawa saldo akhir ke saldo awal bulan berikutnya (`finance_periode`) → HAPUS transaksi bulan itu. Pemicu = timer ke awal bulan + catch-up startup (bukan polling); gagal = ulang tiap 30 menit. Kolom `input_at` (waktu input) **beda** dari `tanggal` (waktu transaksi, bisa backdate); baris lama `input_at` kosong. `rekap_keuangan` rentang terbuka = saldo bulan berjalan (termasuk saldo awal).
- README claims `run.sh` sources `.env`, but current `run.sh` does not — trust the script, not the prose; ensure env reaches the process via the panel environment.

## Deploy (laptop → Pterodactyl panel)

Source lives only on the laptop; the panel gets just the binary. Order: stop bot in panel → `.\scripts\deploy.ps1` (`-y` skips prompt) → start bot. It builds `GOOS=linux GOARCH=amd64 go build -o wabot-go-linux .` then `rclone copyto` to remote `panel:/` with `--inplace` (panel SFTP rejects rename) and verifies remote size. `greeting.mp3`/`.env` are uploaded separately, not via deploy.
