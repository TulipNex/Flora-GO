# AGENTS.md — wabot-go (Flora GO)

WhatsApp bot in Go (`hypermeow` fork of `whatsmeow`, pure-Go `modernc.org/sqlite`, no cgo). Module `wabot-go`, Go 1.26+. Entry: `main.go`. No CI, no lint/typecheck config, no `opencode.json`.

## Run / verify

- `go run .` — but there is **no dotenv loader** (only `os.Getenv`, no godotenv/viper). The `.env` file is never auto-read; export vars in the shell first (see `test-env.ps1` pattern: `$env:OWNER_NUMBERS=...` then `go run .`).
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
- `data/` = session (`session.db`) + SQLite state (group/ai/facts/settings/finance/crm/absensi). Never delete, never commit (gitignored with `.env`, `wabot-go-linux`, `wabot-tmp/`). `push.ps1` warns on sensitive paths — heed it. `test-env.ps1` holds real secrets, stays uncommitted.
- Identity (owner name, bot name) is **compile-time** in `internal/config/config.go` — changing it needs rebuild + redeploy. `BotVersion` is a `var` injected at build time from the `VERSION` file via `-ldflags -X wabot-go/internal/config.BotVersion=...` (fallback = default in config.go); `deploy.ps1` auto-bumps it from changed files via `version-bump.ps1` (new .go = minor, deleted command/tool or Go upgrade = major, other edits = patch, docs/data only = none; snapshot in gitignored `.last-deploy-state`). Provider/model choice is **runtime** in SQLite (`.agent` commands, no redeploy).
- WhatsApp identity is dual-format (LID vs phone number): never compare JIDs by raw string — use `store.ParticipantMatchesJID` / `IsSenderAdmin` / `IsBotAdmin`.
- README claims `run.sh` sources `.env`, but current `run.sh` does not — trust the script, not the prose; ensure env reaches the process via the panel environment.

## Deploy (laptop → Pterodactyl panel)

Source lives only on the laptop; the panel gets just the binary. Order: stop bot in panel → `.\deploy.ps1` (`-y` skips prompt) → start bot. It builds `GOOS=linux GOARCH=amd64 go build -o wabot-go-linux .` then `rclone copyto` to remote `panel:/` with `--inplace` (panel SFTP rejects rename) and verifies remote size. `greeting.mp3`/`.env` are uploaded separately, not via deploy.
