# push.ps1 — push cepat source code wabot-go ke GitHub
# Jalankan dari folder project: .\scripts\push.ps1 "pesan commit"

param(
    [Parameter(Mandatory = $false)]
    [string]$Message
)

# Root proyek = folder induk scripts/. Git harus dijalankan dari root repo,
# jadi arahkan ke sana walau script dipanggil lewat path scripts\push.ps1.
$Root = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $Root

Write-Host "== Mengecek perubahan ==" -ForegroundColor Cyan
git status

if (-not $Message) {
    $Message = Read-Host "Masukkan pesan commit"
}

if ([string]::IsNullOrWhiteSpace($Message)) {
    Write-Host "Pesan commit tidak boleh kosong. Dibatalkan." -ForegroundColor Red
    exit 1
}

Write-Host "== Menambahkan perubahan ==" -ForegroundColor Cyan
git add .

# Tampilkan lagi ringkas apa yang akan di-commit, sebagai pengaman terakhir
$staged = git diff --cached --name-only
if (-not $staged) {
    Write-Host "Tidak ada perubahan untuk di-commit. Selesai." -ForegroundColor Yellow
    exit 0
}

Write-Host "== File yang akan di-commit ==" -ForegroundColor Cyan
$staged | ForEach-Object { Write-Host "  $_" }

# Pengaman: peringatkan kalau file sensitif entah bagaimana ikut ter-stage
$sensitivePatterns = @(".env", "data/", "wabot-go-linux", "wabot-go.md")
$flagged = $staged | Where-Object {
    $file = $_
    $sensitivePatterns | Where-Object { $file -like "*$_*" }
}
if ($flagged) {
    Write-Host "PERINGATAN: file berikut terlihat sensitif dan ikut ter-stage:" -ForegroundColor Red
    $flagged | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    $confirm = Read-Host "Tetap lanjutkan commit & push? (y/n)"
    if ($confirm -ne "y") {
        Write-Host "Dibatalkan." -ForegroundColor Yellow
        exit 1
    }
}

Write-Host "== Commit ==" -ForegroundColor Cyan
git commit -m "$Message"

Write-Host "== Push ke GitHub ==" -ForegroundColor Cyan
git push

Write-Host "Selesai." -ForegroundColor Green
