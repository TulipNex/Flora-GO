module wabot-go

go 1.26.0

require (
	github.com/bogdanfinn/fhttp v0.6.9
	github.com/bogdanfinn/tls-client v1.16.0
	github.com/coder/websocket v1.8.15
	github.com/google/uuid v1.6.0
	github.com/ledongthuc/pdf v0.0.0-20260907135840-6c8c28e0e8a0
	github.com/pdfcpu/pdfcpu v0.16.1
	github.com/polymorfa/hypermeow v0.0.0-20261010085506-b0e8e85a4075
	github.com/polymorfa/libsignal-protocol-go v0.2.3-0.20260806162910-a2adef2e8a11
	github.com/purpshell/meowcaller v0.0.0-20260906234207-c48c3e2a243c
	github.com/robertkrimen/otto v0.5.1
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/xuri/excelize/v2 v2.11.0
	golang.org/x/image v0.46.0
	golang.org/x/net v0.59.0
	google.golang.org/protobuf v1.36.12
	modernc.org/sqlite v1.58.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/bdandy/go-errors v1.2.2 // indirect
	github.com/bdandy/go-socks4 v1.2.3 // indirect
	github.com/bogdanfinn/quic-go-utls v1.0.10-utls // indirect
	github.com/bogdanfinn/utls v1.7.8-barnius // indirect
	github.com/bogdanfinn/websocket v1.5.6-barnius // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudflare/circl v1.6.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/hajimehoshi/go-mp3 v0.3.4 // indirect
	github.com/hhrutter/tiff v1.0.7 // indirect
	github.com/klauspost/compress v1.18.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/petermattis/goid v0.0.0-20260820044319-269ab09b5261 // indirect
	github.com/pion/datachannel v1.6.0 // indirect
	github.com/pion/dtls/v3 v3.1.2 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/opus v0.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.9.4 // indirect
	github.com/pion/transport/v4 v4.0.1 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/richardlehane/mscfb v1.0.7 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/tam7t/hpkp v0.0.0-20160821193359-2b70b4024ed5 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	go.mau.fi/util v0.10.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/sourcemap.v1 v1.0.5 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// replace hypermeow dengan clone lokal ter-patch (third_party/hypermeow,
// pin b0e8e85a = require di atas): satu-satunya perubahan adalah
// buildNativeFlowBizNode selalu mengirim native_flow name="mixed" (paritas
// Baileys) karena derivasi nama dari tombol ditolak server (473/405).
// Saat update fork: ulangi clone dangkal di pin baru + terapkan ulang patch
// (diff kecil di send.go), lalu sesuaikan require + komentar ini.
replace github.com/polymorfa/hypermeow => ./third_party/hypermeow
