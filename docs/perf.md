# Diagnostik performa (Tahap 0)

## pprof (opsional, default mati)

Aktifkan dengan env `PPROF_ADDR` (hanya localhost):

```powershell
$env:PPROF_ADDR="127.0.0.1:6060"
go run .
```

Lalu buka profil:

```powershell
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
go tool pprof http://127.0.0.1:6060/debug/pprof/goroutine
```

Keamanan: host selain localhost/127.0.0.1/::1 dipaksa ke 127.0.0.1. Kosongkan env untuk mematikan.

## Benchmark

```powershell
go test -bench=. -benchmem ./internal/identity/ ./internal/textx/
```

## Verifikasi dasar

```powershell
go build ./...
go vet ./...
go test ./...
```

Catatan: race detector (`go test -race`) membutuhkan cgo (gcc) yang tidak tersedia di panel Windows ini; jalankan di environment dengan gcc bila perlu.