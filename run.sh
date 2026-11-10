#!/bin/bash
# run.sh — startup script untuk dipakai sebagai "Startup Command" di Pterodactyl.
#
# Mode 1 (disarankan): jalankan binary hasil build dari laptop (./wabot-go-linux)
# Mode 2 (fallback)   : jika binary tidak ada tapi Go tersedia di container, build otomatis
#
# PENTING: binary dijalankan via `exec` supaya MENGGANTIKAN proses bash ini
# sebagai PID 1 container. Tanpa exec, sinyal stop panel (SIGTERM) berhenti
# di bash dan tidak sampai ke bot — shutdown jadi tidak bersih (SIGKILL),
# yang dibaca panel sebagai crash dan me-restart server sendiri.

mkdir -p data

# Load environment variable dari file .env (bash tidak otomatis membacanya,
# harus di-export manual). Tanpa ini, LOGIN_METHOD dan PHONE_NUMBER di .env
# tidak akan pernah sampai ke proses Go, walau file .env-nya sudah benar.
if [ -f ".env" ]; then
    echo "📄 Memuat variabel dari .env..."
    # Hapus carriage return Windows (\r) jika file diedit/diunggah dari Windows
    sed -i 's/\r$//' .env 2>/dev/null || true
    set -a
    source .env
    set +a
else
    echo "⚠️  File .env tidak ditemukan di $(pwd)."
fi

# PENTING: paksa cache/module Go ke folder di LUAR direktori project ini
# (/home/container/.go-external, bukan ./go/pkg/mod). Kalau tidak di-set,
# beberapa image container menaruh GOPATH default di dalam folder project,
# sehingga Go ikut membaca folder cache module sebagai bagian dari source
# code kita sendiri dan build gagal dengan error aneh soal "@version".
export GOPATH="/home/container/.go-external"
export GOMODCACHE="/home/container/.go-external/pkg/mod"
export GOCACHE="/home/container/.go-external/cache"
mkdir -p "$GOMODCACHE" "$GOCACHE"

# Bersihkan folder cache Go lama yang (dari percobaan sebelumnya) sempat
# tercampur ke dalam direktori project ini. Aman dihapus, isinya cuma cache.
if [ -d "./go" ]; then
    echo "🧹 Membersihkan folder cache Go lama yang tercampur di project..."
    rm -rf ./go
fi
if [ -d "./.cache" ]; then
    rm -rf ./.cache
fi

# Versi dari file VERSION (disuntik via ldflags; fallback: default di config.go).
LDFLAGS=""
if [ -f "./VERSION" ]; then
    VER="$(tr -d ' \r\n\t' < ./VERSION)"
    if [ -n "$VER" ]; then
        LDFLAGS="-X wabot-go/internal/config.BotVersion=$VER"
    fi
fi

if [ -f "./wabot-go-linux" ]; then
    echo "▶️  Menjalankan binary precompiled..."
    chmod +x ./wabot-go-linux
    exec ./wabot-go-linux
elif command -v go >/dev/null 2>&1; then
    echo "▶️  Binary tidak ditemukan, build dari source menggunakan Go di container..."

    # Jika go.sum belum ada / dependency belum terkunci, resolve versi asli dulu.
    if [ ! -f "go.sum" ]; then
        echo "📦 Mengambil versi dependency terbaru (sekali saja, agak lama)..."
        go get github.com/polymorfa/hypermeow@main
        go get github.com/purpshell/meowcaller@latest
        go get modernc.org/sqlite@latest
        go get google.golang.org/protobuf@latest
    fi

    go mod tidy
    go build -ldflags "$LDFLAGS" -o wabot-go-linux .
    chmod +x ./wabot-go-linux
    exec ./wabot-go-linux
else
    echo "❌ Tidak ada binary 'wabot-go-linux' dan Go tidak terinstall di container ini."
    echo "   Build dulu di laptop dengan: GOOS=linux GOARCH=amd64 go build -o wabot-go-linux ."
    echo "   Lalu upload file 'wabot-go-linux' ke panel ini."
    exit 1
fi
