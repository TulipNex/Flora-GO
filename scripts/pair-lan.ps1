<#
.SYNOPSIS
  Generate opencode pairing link/QR yang bisa di-scan dari HP satu Wi-Fi.

.DESCRIPTION
  Otomatis mendeteksi IPv4 Wi-Fi laptop + port service opencode,
  lalu menjalankan: opencode pair --url http://<IP>:<port>
  Scan QR dari HP segera (link hanya berlaku 5 menit, sekali pakai).

.PARAMETER IPAddress
  Override IP manual kalau deteksi otomatis salah (mis. -IPAddress 192.168.1.50).

.PARAMETER InterfaceAlias
  Nama interface yang dipakai untuk deteksi IP (default: Wi-Fi).

.PARAMETER BindLan
  Otomatis set service hostname ke 0.0.0.0 + restart service bila masih
  ter-bind ke localhost. Tanpa switch ini script hanya memberi peringatan.

.EXAMPLE
  .\pair-lan.ps1
.EXAMPLE
  .\pair-lan.ps1 -BindLan
.EXAMPLE
  .\pair-lan.ps1 -IPAddress 192.168.1.50
#>
param(
  [string]$IPAddress = "",
  [string]$InterfaceAlias = "Wi-Fi",
  [switch]$BindLan
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command opencode -ErrorAction SilentlyContinue)) {
  Write-Error "Perintah 'opencode' tidak ditemukan di PATH."
}

# 1. Pastikan service ter-bind ke LAN (bukan localhost saja)
$hostname = (opencode service get hostname 2>$null | Out-String).Trim()
if ([string]::IsNullOrWhiteSpace($hostname)) { $hostname = "127.0.0.1" }
if ($hostname -eq "127.0.0.1" -or $hostname -eq "localhost") {
  if ($BindLan) {
    Write-Host "Service masih di $hostname, ubah ke 0.0.0.0 + restart..."
    opencode service set hostname 0.0.0.0
    opencode service restart | Out-Null
  } else {
    Write-Warning "Service ter-bind ke $hostname (hanya laptop sendiri)."
    Write-Warning "Jalankan ulang dengan -BindLan, atau manual: opencode service set hostname 0.0.0.0; opencode service restart"
    exit 1
  }
}

# 2. Deteksi IP LAN
if ([string]::IsNullOrWhiteSpace($IPAddress)) {
  $IPAddress = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
    Where-Object { $_.InterfaceAlias -like "*$InterfaceAlias*" -and $_.IPAddress -notlike "169.254*" } |
    Select-Object -ExpandProperty IPAddress -First 1
}
if ([string]::IsNullOrWhiteSpace($IPAddress)) {
  Write-Error "IP Wi-Fi tidak ketemu. Isi manual: .\pair-lan.ps1 -IPAddress 192.168.1.x"
}

# 3. Deteksi port service
$status = (opencode service status 2>&1 | Out-String)
$port = "45278"
if ($status -match ":(\d+)") { $port = $Matches[1] }

$url = "http://${IPAddress}:${port}"
Write-Host "Pairing URL: $url"
opencode pair --url $url
