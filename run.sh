#!/bin/bash
# run.sh — startup script untuk dipakai sebagai "Startup Command" di Pterodactyl.
#
# Mode 1 (disarankan): jalankan binary hasil build dari laptop (./wabot-go-linux)
# Mode 2 (fallback)   : jika binary tidak ada tapi Go tersedia di container, build otomatis

mkdir -p data

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
    ./wabot-go-linux
elif command -v go >/dev/null 2>&1; then
    echo "▶️  Binary tidak ditemukan, build dari source menggunakan Go di container..."
    go mod tidy
    go build -ldflags "$LDFLAGS" -o wabot-go-linux .
    chmod +x ./wabot-go-linux
    ./wabot-go-linux
else
    echo "❌ Tidak ada binary 'wabot-go-linux' dan Go tidak terinstall di container ini."
    echo "   Build dulu di laptop dengan: GOOS=linux GOARCH=amd64 go build -o wabot-go-linux ."
    echo "   Lalu upload file 'wabot-go-linux' ke panel ini."
    exit 1
fi
