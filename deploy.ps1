# deploy.ps1
# Build wabot-go untuk Linux, lalu sync binary-nya ke panel Pterodactyl via SFTP (rclone).
# Jalankan dari root folder project (tempat main.go berada).

$ErrorActionPreference = "Stop"

$BinaryName = "wabot-go-linux"
$RemoteName = "panel"                # nama remote rclone yang sudah dikonfigurasi
$RemotePath = "${RemoteName}:/"      # binary diletakkan di root panel

# ---------- Helper tampilan ----------

function Write-Divider {
    Write-Host ("─" * 65) -ForegroundColor DarkGray
}

# Banner ASCII besar "FLORA GO", ditampilkan sekali di awal sebelum proses build/sync mulai.
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

# ---------- Build ----------

Write-Host ""
Write-Banner
Write-Header "DEPLOY SCRIPT"
Write-Step "Building $BinaryName (linux/amd64)..."

$env:GOOS = "linux"
$env:GOARCH = "amd64"

go build -o $BinaryName .

if ($LASTEXITCODE -ne 0) {
    Write-Err "Build gagal. Sync dibatalkan."
    exit 1
}

$fileInfo = Get-Item $BinaryName
$sizeText = Format-Size $fileInfo.Length
Write-Ok "Build sukses  ($sizeText)"

# ---------- Sync ----------

Write-Host ""
Write-Step "Sync ke $RemotePath$BinaryName ..."
Write-Host ""

# Fungsi untuk menggambar bar blok custom: BLOKBLOKBLOK----  20.6 MB / 29.1 MB   96 KiB/s   ETA 0:08
# BarWidth tetap (20 blok), sisanya terisi karakter kosong berwarna abu-abu.
function Write-SyncBar {
    param(
        [double]$TransferredMB,
        [double]$TotalMB,
        [string]$Speed,
        [string]$Eta
    )
    $barWidth = 20
    $ratio = if ($TotalMB -gt 0) { $TransferredMB / $TotalMB } else { 0 }
    if ($ratio -gt 1) { $ratio = 1 }
    $filled = [Math]::Round($barWidth * $ratio)
    $empty = $barWidth - $filled

    Write-Host "`r " -NoNewline
    Write-Host ("$([char]0x2593)" * $filled) -NoNewline -ForegroundColor Green
    Write-Host ("$([char]0x2593)" * $empty) -NoNewline -ForegroundColor DarkGray
    $statsText = "  {0:N1}/{1:N1} MB   {2}   ETA {3}" -f $TransferredMB, $TotalMB, $Speed, $Eta
    # `e[0K` (ANSI "erase to end of line") menghapus sisa karakter dari baris
    # sebelumnya. PadRight(50) ditambahkan sebagai jaring pengaman kalau ANSI
    # escape ini tidak didukung terminal tertentu (jarang terjadi di Windows
    # Terminal/PowerShell modern, tapi lebih aman ada dua lapis).
    Write-Host "$($statsText.PadRight(50))$([char]27)[0K" -NoNewline
}

# --sftp-set-modtime=false        : beberapa server SFTP di Pterodactyl menolak set modtime, ini menghindari error terkait itu.
# --inplace                       : tulis langsung ke nama file final, TANPA lewat file .partial + rename.
#                                    Server SFTP panel ini menolak operasi rename (SSH_FX_FAILURE), jadi rename dihindari sepenuhnya.
# --sftp-disable-concurrent-reads/writes : beberapa server SFTP custom kurang stabil dengan multi-stream, ini pakai satu koneksi saja.
# --retries 3 --low-level-retries 5 : mengantisipasi koneksi SFTP yang sempat timeout.
# --stats 250ms                    : rclone mencetak blok statistik tiap 250ms ke stderr (dengan newline asli, penting
#                                    supaya ReadLine() tidak menunggu tanpa akhir). Setiap baris di-parse lalu
#                                    digambar ulang sebagai bar blok di baris yang sama pakai `r.
# -v                                : perlu supaya baris "Transferred:" per-file ikut tercetak, bukan cuma ringkasan akhir.
$totalMB = $fileInfo.Length / 1MB
$rcloneArgs = @(
    "copyto", $BinaryName, "$RemotePath$BinaryName",
    "--sftp-set-modtime=false",
    "--inplace",
    "--sftp-disable-concurrent-reads",
    "--sftp-disable-concurrent-writes",
    "--retries", "3",
    "--low-level-retries", "5",
    "--stats", "250ms",
    "-v"
)

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = "rclone"
$psi.Arguments = ($rcloneArgs | ForEach-Object { if ($_ -match '\s') { "`"$_`"" } else { $_ } }) -join " "
$psi.RedirectStandardError = $true
$psi.RedirectStandardOutput = $false
$psi.UseShellExecute = $false

$proc = New-Object System.Diagnostics.Process
$proc.StartInfo = $psi
[Console]::CursorVisible = $false

try {
    $proc.Start() | Out-Null

    # rclone menulis baris statistik ke stderr. Setiap baris di-parse untuk ambil
    # jumlah MB terkirim, kecepatan, dan ETA, lalu digambar ulang sebagai bar blok
    # di baris yang sama (pakai `r, tanpa pindah baris) supaya terlihat "hidup".
    while (-not $proc.StandardError.EndOfStream) {
        $line = $proc.StandardError.ReadLine()

        # Contoh baris asli rclone: "Transferred:   	   13.145 MiB / 29.069 MiB, 45%, 92.481 KiB/s, ETA 28s"
        if ($line -match "Transferred:\s+([\d\.]+)\s*MiB\s*/\s*([\d\.]+)\s*MiB.*?,\s*([\d\.]+\s*\wiB/s),\s*ETA\s*(\S+)") {
            $doneMB = [double]$matches[1]
            $totalMBFromLine = [double]$matches[2]
            $speed = $matches[3]
            $eta = $matches[4]
            Write-SyncBar -TransferredMB $doneMB -TotalMB $totalMBFromLine -Speed $speed -Eta $eta
        }
    }

    $proc.WaitForExit()
    $syncExitCode = $proc.ExitCode

    # Pastikan bar berakhir penuh (100%) sebelum lanjut, lalu pindah baris.
    Write-SyncBar -TransferredMB $totalMB -TotalMB $totalMB -Speed "" -Eta "0s"
}
finally {
    # Cursor selalu dikembalikan, bahkan kalau proses dihentikan paksa (Ctrl+C)
    # atau terjadi error tak terduga di tengah pembacaan output.
    [Console]::CursorVisible = $true
}

Write-Host ""
Write-Host ""

if ($syncExitCode -ne 0) {
    Write-Err "Sync gagal."
    exit 1
}

$sw.Stop()
$elapsed = "{0:mm\:ss}" -f $sw.Elapsed

Write-Ok "Sync selesai"
Write-Host ""
Write-Divider
Write-Host "  Binary   : $BinaryName" -ForegroundColor White
Write-Host "  Ukuran   : $sizeText" -ForegroundColor White
Write-Host "  Target   : $RemotePath$BinaryName" -ForegroundColor White
Write-Host "  Durasi   : $elapsed" -ForegroundColor White
Write-Divider
Write-Host "  Deploy sukses ✔" -ForegroundColor Green
Write-Divider
Write-Host ""