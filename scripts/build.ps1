# build.ps1
# Build binary Linux untuk panel — TANPA sync/upload. Hasilnya di root proyek
# (`wabot-go-linux`), siap diunggah manual lewat File Manager panel Pterodactyl.
#
# Jalankan dari root project:  .\scripts\build.ps1
# (script otomatis mengarahkan direktori kerja ke root, tempat main.go berada).
#
# Versi: sama seperti deploy.ps1 — OTOMATIS dari tanggal + urutan build hari
# itu (CalVer v<bulan>.<tanggal>.<urut>), hanya naik bila ada file kode/tooling
# berubah sejak build/deploy terakhir (version-bump.ps1).
#
#   -Bump auto (default) | none | vX.Y.Z   override seperti deploy.ps1
#   -Keep                                  pakai VERSION sekarang, TANPA
#                                          menulis VERSION/.last-deploy-state
#                                          (untuk build coba-coba yang tidak
#                                          di-upload; tidak "memakan" versi)
#   -Out nama                              nama file output (default wabot-go-linux)
#
# Contoh:
#   .\scripts\build.ps1                     # build + bump versi otomatis
#   .\scripts\build.ps1 -Keep               # build pakai versi sekarang
#   .\scripts\build.ps1 -Bump none          # build tanpa bump, state tetap ditulis
param(
    [string]$Bump = "auto",
    [switch]$Keep,
    [string]$Out = "wabot-go-linux"
)

$ErrorActionPreference = "Stop"

# Root proyek = folder induk scripts/. Semua operasi (go build, output) relatif
# terhadap root, jadi arahkan langsung ke sana supaya script tetap benar walau
# dipanggil lewat path scripts\build.ps1.
$Root = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $Root

# ---------- Helper tampilan ----------

function Write-Divider {
    Write-Host ("─" * 65) -ForegroundColor DarkGray
}

# Banner ASCII "FLORA GO" — sama dengan deploy.ps1 supaya tampilannya konsisten.
function Write-Banner {
    $banner = @"
███████╗██╗      ██████╗ ██████╗  █████╗      ██████╗  ██████╗
██╔════╝██║     ██╔═══██╗██╔══██╗██╔══██╗    ██╔════╝ ██╔═══██╗
█████╗  ██║     ██║   ██║██████╔╝███████║    ██║  ███╗██║   ██║
██╔══╝  ██║     ██║   ██║██╔══██╗██╔══██║    ██║   ██║██║   ██║
██║     ███████╗╚██████╔╝██║  ██║██║  ██║    ╚██████╔╝╚██████╔╝
╚═╝     ╚══════╝ ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝     ╚═════╝  ╚═════╝
"@
    Write-Host $banner -ForegroundColor Green
}

function Write-Header {
    param([string]$Title)
    Write-Divider
    Write-Host "  $Title" -ForegroundColor Cyan
    Write-Divider
}

function Write-Step {
    param([string]$Text)
    Write-Host " → $Text" -ForegroundColor Yellow
}

function Write-Ok {
    param([string]$Text)
    Write-Host " ✔ $Text" -ForegroundColor Green
}

function Write-Err {
    param([string]$Text)
    Write-Host " ✘ $Text" -ForegroundColor Red
}

function Format-Size {
    param([long]$Bytes)
    if ($Bytes -ge 1MB) { return "{0:N2} MB" -f ($Bytes / 1MB) }
    elseif ($Bytes -ge 1KB) { return "{0:N2} KB" -f ($Bytes / 1KB) }
    else { return "$Bytes B" }
}

$sw = [System.Diagnostics.Stopwatch]::StartNew()
$startTime = Get-Date

Write-Host ""
Write-Banner
Write-Header "BUILD SCRIPT (tanpa upload)"

# ---------- Versi ----------
# -Keep sengaja melewati version-bump.ps1 supaya VERSION dan snapshot
# .last-deploy-state tidak tersentuh — build percobaan tidak boleh "memakan"
# nomor versi yang seharusnya dipakai saat deploy sungguhan.

Write-Step "Menentukan versi..."

if ($Keep) {
    if (-not (Test-Path "VERSION")) {
        Write-Err "VERSION tidak ditemukan — jalankan tanpa -Keep agar dibuat otomatis."
        exit 1
    }
    $BotVersion = (Get-Content "VERSION" -Raw).Trim()
    if ($BotVersion -eq "") { $BotVersion = "v0.1.0" }
} else {
    . "$PSScriptRoot\version-bump.ps1"
    try {
        $ver = Invoke-VersionBump -Override $Bump
    } catch {
        Write-Err "Gagal menentukan versi: $($_.Exception.Message)"
        exit 1
    }
    $BotVersion = $ver.NewVersion
    if ($ver.Level -eq "none") {
        Write-Ok "Versi tetap $($ver.OldVersion) (tidak ada perubahan kode)"
    } else {
        Write-Ok "Versi $($ver.OldVersion) -> $($ver.NewVersion) (CalVer)"
        Write-Host "  Alasan: $($ver.Reason)" -ForegroundColor DarkGray
    }
}

if ($Keep) {
    Write-Ok "Versi $BotVersion (VERSION/state tidak diubah)"
}

# ---------- Build ----------

Write-Step "Building $Out (linux/amd64) [$BotVersion]..."

$env:GOOS = "linux"
$env:GOARCH = "amd64"
# Projek ini murni Go (modernc.org/sqlite, tanpa cgo). Dipaksa 0 supaya build
# lintas-platform tidak bergantung pada toolchain C di mesin Windows.
$env:CGO_ENABLED = "0"

go build -ldflags "-X wabot-go/internal/config.BotVersion=$BotVersion" -o $Out .

if ($LASTEXITCODE -ne 0) {
    Write-Err "Build gagal."
    exit 1
}

$fileInfo = Get-Item $Out
$sizeText = Format-Size $fileInfo.Length

# Cek magic ELF (7F 45 4C 46). Kalau salah target, binary tidak akan jalan di
# panel — lebih baik ketahuan di sini daripada setelah upload.
$isElf = $false
try {
    $fs = [System.IO.File]::OpenRead($fileInfo.FullName)
    try {
        $magic = New-Object byte[] 4
        [void]$fs.Read($magic, 0, 4)
        $isElf = ($magic[0] -eq 0x7F -and $magic[1] -eq 0x45 -and $magic[2] -eq 0x4C -and $magic[3] -eq 0x46)
    } finally { $fs.Close() }
} catch {}

Write-Ok "Build sukses ($sizeText)"
if (-not $isElf) {
    Write-Err "PERINGATAN: file tidak ber-magic ELF — kemungkinan target build salah."
}

# SHA-256 berguna untuk memastikan file yang ter-upload di panel memang file ini.
$hash = (Get-FileHash -Path $fileInfo.FullName -Algorithm SHA256).Hash.ToLower()

$sw.Stop()
$elapsed = "{0:mm\:ss}" -f $sw.Elapsed
$startText = $startTime.ToString("dd-MM-yyyy HH:mm:ss")
$finishTime = Get-Date -Format "dd-MM-yyyy HH:mm:ss"

# ---------- Ringkasan ----------

Write-Host ""
Write-Divider
Write-Host "  File     : $($fileInfo.FullName)" -ForegroundColor White
Write-Host "  Versi    : $BotVersion" -ForegroundColor White
Write-Host "  Ukuran   : $sizeText" -ForegroundColor White
Write-Host "  SHA-256  : $hash" -ForegroundColor DarkGray
Write-Host "  Durasi   : $elapsed" -ForegroundColor White
Write-Host "  Mulai    : $startText" -ForegroundColor White
Write-Host "  Selesai  : $finishTime" -ForegroundColor White
Write-Divider
Write-Host "  Langkah upload manual:" -ForegroundColor Cyan
Write-Host "   1. Stop bot di panel Pterodactyl." -ForegroundColor DarkYellow
Write-Host "   2. Upload '$Out' ke folder root panel (timpa file lama)." -ForegroundColor DarkYellow
Write-Host "   3. Start bot, lalu cek versi lewat perintah bot." -ForegroundColor DarkYellow
Write-Divider
Write-Host "  Build selesai ✔" -ForegroundColor Green
Write-Divider
Write-Host ""
