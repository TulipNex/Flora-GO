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
#
# PENTING: seluruh baris (carriage return + bar + teks + clear-to-end) ditulis
# sebagai SATU string lewat [Console]::Write() dalam SATU panggilan. Versi
# sebelumnya memakai beberapa panggilan Write-Host terpisah (satu per segmen
# warna) — di terminal integrated VS Code, tiap panggilan Write-Host bisa
# di-flush terpisah ke buffer, sehingga `r di awal tidak selalu "reset" posisi
# kursor sebelum segmen berikutnya ditulis. Hasilnya baris menumpuk ke samping
# alih-alih saling menimpa. Warna di sini memakai kode ANSI manual supaya tetap
# satu string utuh.
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

    $esc = [char]27
    $green = "$esc[32m"
    $gray = "$esc[90m"
    $reset = "$esc[0m"
    $clearEol = "$esc[0K"

    $filledBlocks = "$([char]0x2593)" * $filled
    $emptyBlocks = "$([char]0x2593)" * $empty
    $statsText = "  {0:N1}/{1:N1} MB   {2}   ETA {3}" -f $TransferredMB, $TotalMB, $Speed, $Eta

    $line = "`r $green$filledBlocks$gray$emptyBlocks$reset$statsText$clearEol"
    [Console]::Write($line)
}

# --sftp-set-modtime=false        : beberapa server SFTP di Pterodactyl menolak set modtime, ini menghindari error terkait itu.
# --inplace                       : tulis langsung ke nama file final, TANPA lewat file .partial + rename.
#                                    Server SFTP panel ini menolak operasi rename (SSH_FX_FAILURE), jadi rename dihindari sepenuhnya.
# --sftp-disable-concurrent-reads/writes : beberapa server SFTP custom kurang stabil dengan multi-stream, ini pakai satu koneksi saja.
# --retries 3 --low-level-retries 5 : mengantisipasi koneksi SFTP yang sempat timeout.
# --stats 250ms                    : rclone mencetak blok statistik tiap 250ms ke stderr (dengan newline asli, penting
#                                    supaya ReadLine() tidak menunggu tanpa akhir). Setiap baris di-parse lalu
#                                    digambar ulang sebagai bar blok di baris yang sama pakai `r.
# -vv                               : debug level, perlu supaya pesan error detail dari layer SFTP (mis. SSH_FX_FAILURE,
#                                    permission denied) ikut tercetak ke stderr, bukan cuma exit code tanpa keterangan.
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
    "-vv"
)

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = "rclone"
$psi.Arguments = ($rcloneArgs | ForEach-Object { if ($_ -match '\s') { "`"$_`"" } else { $_ } }) -join " "
$psi.RedirectStandardError = $true
$psi.RedirectStandardOutput = $false
$psi.UseShellExecute = $false
# PENTING: ProcessStartInfo TIDAK otomatis mewarisi direktori kerja PowerShell
# saat ini. Tanpa baris ini, rclone mencari $BinaryName relatif terhadap
# direktori kerja default proses (biasanya home folder user Windows),
# bukan folder project tempat script ini dijalankan — menyebabkan error
# "directory not found" walau file binary-nya sebenarnya ada.
$psi.WorkingDirectory = (Get-Location).Path

$proc = New-Object System.Diagnostics.Process
$proc.StartInfo = $psi
# Dibungkus try-catch karena beberapa terminal (termasuk sebagian konfigurasi
# integrated terminal) melempar exception saat CursorVisible diakses/diubah.
# Kegagalan di sini tidak fatal — bar tetap jalan, hanya cursor tetap kedip.
try { [Console]::CursorVisible = $false } catch {}

# Semua baris stderr ditampung di sini. Baris statistik progres (yang cocok
# regex di bawah) dipakai untuk menggambar bar; baris LAINNYA disimpan sebagai
# kandidat pesan error, supaya kalau sync gagal kita punya sesuatu yang bisa
# ditunjukkan ke user selain cuma "ExitCode != 0".
$logLines = New-Object System.Collections.Generic.List[string]

try {
    $proc.Start() | Out-Null

    # rclone menulis baris statistik ke stderr. Setiap baris di-parse untuk ambil
    # jumlah MB terkirim, kecepatan, dan ETA, lalu digambar ulang sebagai bar blok
    # di baris yang sama (pakai `r, tanpa pindah baris) supaya terlihat "hidup".
    while (-not $proc.StandardError.EndOfStream) {
        $line = $proc.StandardError.ReadLine()
        $logLines.Add($line)

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
    try { [Console]::CursorVisible = $true } catch {}
}

Write-Host ""
Write-Host ""

if ($syncExitCode -ne 0) {
    Write-Err "Sync gagal (exit code $syncExitCode)."
    Write-Host ""

    # Baris "penyebab error" = baris stderr yang BUKAN baris statistik biasa
    # (Transferred/Elapsed time/Errors/Checks ringkasan), karena baris-baris
    # itu normal muncul di setiap run sukses maupun gagal. Sisanya (mis. pesan
    # "SSH_FX_FAILURE", "permission denied", "context deadline exceeded", dll)
    # kemungkinan besar adalah pesan error sesungguhnya dari rclone.
    $errorCandidates = $logLines | Where-Object {
        $_.Trim() -ne "" -and
        $_ -notmatch "^\s*Transferred:" -and
        $_ -notmatch "^\s*Elapsed time:" -and
        $_ -notmatch "^\s*Errors:\s*0" -and
        $_ -notmatch "^\s*Checks:" -and
        $_ -notmatch "^\s*Deleted:" -and
        $_ -notmatch "^\s*Renamed:" -and
        $_ -notmatch "^\s*Server side copies:"
    }

    if ($errorCandidates.Count -gt 0) {
        Write-Host " Log penyebab error dari rclone:" -ForegroundColor Red
        Write-Divider
        foreach ($l in $errorCandidates) {
            Write-Host " $l" -ForegroundColor DarkYellow
        }
        Write-Divider
    } else {
        Write-Host " (rclone tidak mengirim pesan error rinci meski sudah pakai -vv." -ForegroundColor DarkGray
        Write-Host "  Kemungkinan koneksi terputus total sebelum sempat log apa pun.)" -ForegroundColor DarkGray
    }
    Write-Host ""
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