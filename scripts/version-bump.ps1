# version-bump.ps1 - penentu versi otomatis berbasis tanggal deploy (CalVer).
#
# Dipakai oleh deploy.ps1 sebelum build. Logika: bandingkan kondisi kerja
# saat ini (commit + worktree + untracked) dengan snapshot deploy terakhir
# (.last-deploy-state). Kalau TIDAK ADA file kode/tooling yang berubah, versi
# dibiarkan (tidak ada bump ganda). Kalau ADA perubahan, versi baru dibuat
# dari tanggal hari ini + nomor urut deploy hari itu:
#
#     v<bulan>.<tanggal>.<urut>     contoh: v10.18.1, lalu v10.18.2
#
# Tanggal memakai waktu lokal mesin, tanpa nol di depan. Nomor urut reset
# tiap ganti hari (besok: v10.19.1).
#
# Hanya perubahan file kode/tooling yang dihitung. Perubahan yang HANYA
# menyentuh dokumen/data (README, AGENTS, data/, .env, VERSION, state,
# binary, dll) tidak menaikkan versi — lihat Test-Excluded.
#
# Cara pakai langsung (dry-run, tanpa menulis apa pun):
#   .\scripts\version-bump.ps1 -NoUpdate
# Dipakai dari script lain (dot-source, mengembalikan objek):
#   . .\scripts\version-bump.ps1
#   $v = Invoke-VersionBump -Override auto
param(
    [string]$Override = "auto",
    [string]$VersionFile = "VERSION",
    [string]$StateFile = ".last-deploy-state",
    [switch]$NoUpdate
)

$ErrorActionPreference = "Stop"
# Script ini berada di scripts/ -> root proyek = satu level di atasnya
# (tempat VERSION, .last-deploy-state, go.mod, main.go berada).
$Root = if ($PSScriptRoot) { Split-Path -Parent $PSScriptRoot } else { (Get-Location).Path }
Set-Location -LiteralPath $Root

# Semua pemanggilan git lewat satu pintu ini: menonaktifkan autocrlf
# per-command (menghilangkan warning "LF will be replaced by CRLF" yang
# memenuhi stderr dan — dengan $ErrorActionPreference=Stop — menjadi
# terminating error yang mengosongkan hasil), serta memaksa EAP=Continue
# selama proses native berjalan.
function Invoke-Git {
    param([string[]]$GitArgs)
    $prev = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $out = & git -c core.quotepath=false -c core.autocrlf=false @GitArgs 2>&1
        $code = $LASTEXITCODE
        $lines = @($out | ForEach-Object { "$_" } | Where-Object { $_ -ne "" -and $_ -notmatch '^warning:' })
        return @{ Code = $code; Lines = $lines }
    } catch { return @{ Code = 1; Lines = @() } }
    finally { $ErrorActionPreference = $prev }
}

function Get-GitLines {
    param([string[]]$GitArgs)
    $r = Invoke-Git -GitArgs $GitArgs
    if ($r.Code -ne 0) { return @() }
    return @($r.Lines | ForEach-Object { "$_".Trim() } | Where-Object { $_ -ne "" })
}

function Get-GitText {
    param([string[]]$GitArgs)
    $r = Invoke-Git -GitArgs $GitArgs
    if ($r.Code -ne 0) { return "" }
    return ($r.Lines -join "`n")
}

function Read-CurrentVersion {
    param([string]$Path)
    if (Test-Path $Path) {
        $raw = (Get-Content $Path -Raw).Trim() -replace '\s+', ''
        if ($raw -match '^v?\d+\.\d+\.\d+$') {
            if ($raw -notmatch '^v') { $raw = "v$raw" }
            return $raw
        }
    }
    # Fallback: parse default dari internal/config/config.go
    $cfg = Join-Path $Root "internal/config/config.go"
    if (Test-Path $cfg) {
        $m = Select-String -Path $cfg -Pattern 'BotVersion\s*=\s*"(v?\d+\.\d+\.\d+)"' | Select-Object -First 1
        if ($m) {
            $v = $m.Matches[0].Groups[1].Value
            if ($v -notmatch '^v') { $v = "v$v" }
            return $v
        }
    }
    return "v0.1.0"
}

# New-DeployVersion membuat versi CalVer dari waktu lokal: v<bulan>.<tanggal>.<urut>
# tanpa nol di depan. Kalau versi lama bertanggal sama (bulan.tanggal sama),
# urutannya dilanjutkan; kalau beda hari (atau versi lama bukan format ini),
# mulai dari 1.
function New-DeployVersion {
    param([string]$OldVersion)
    $now = Get-Date
    $n = 1
    if ($OldVersion -match '^v?(\d+)\.(\d+)\.(\d+)$') {
        $om = [int]$Matches[1]; $od = [int]$Matches[2]; $on = [int]$Matches[3]
        if ($om -eq $now.Month -and $od -eq $now.Day) { $n = $on + 1 }
    }
    return "v$($now.Month).$($now.Day).$n"
}

# File yang TIDAK dihitung sebagai pemicu versi: dokumen, data sesi/state,
# .env, binary build, file temp/db. Perubahan hanya di sini => versi tetap.
function Test-Excluded {
    param([string]$Rel)
    $p = $Rel -replace '\\', '/'
    if ($p -match '^(README\.md|AGENTS\.md|\.gitignore|VERSION|\.last-deploy-state|qr\.png|\.env|test-env\.ps1)$') { return $true }
    if ($p -match '^(data|wabot-tmp|apps-script)/') { return $true }
    if ($p -match '^(wabot-go-linux|wabot-test|wabot-go\.exe)') { return $true }
    if ($p -match '\.(exe|log|db|db-shm|db-wal)$') { return $true }
    return $false
}

function Invoke-VersionBump {
    param(
        [string]$Override = "auto",
        [string]$VersionFile = "VERSION",
        [string]$StateFile = ".last-deploy-state",
        [switch]$NoUpdate
    )

    $VersionPath = Join-Path $Root $VersionFile
    $StatePath = Join-Path $Root $StateFile
    $oldVersion = Read-CurrentVersion -Path $VersionPath

    $head = Get-GitText -GitArgs @("rev-parse", "HEAD")
    $head = $head.Trim()
    $gitOk = ($head -ne "" -and $head -match '^[0-9a-f]{40}$')

    # Muat snapshot deploy terakhir: commit + map path=hash yang sudah "dikonsumsi".
    $consumed = @{}
    $baseCommit = ""
    if (Test-Path $StatePath) {
        foreach ($line in (Get-Content $StatePath)) {
            $t = "$line".Trim()
            if ($t -match '^commit=(.+)$') { $baseCommit = $matches[1].Trim() }
            elseif ($t -match '^(.+)=([0-9a-f]{40}|DELETED)$') { $consumed[$matches[1]] = $matches[2] }
        }
    }
    if ($baseCommit -eq "" -or -not $gitOk) { $baseCommit = $head }
    $haveBase = ($gitOk -and $baseCommit -ne "" -and $baseCommit -ne $head)

    # Kumpulkan file berubah: commit sejak baseline + worktree + staged-new + untracked.
    $changed = @{}
    if ($haveBase) {
        $chk = Invoke-Git -GitArgs @("cat-file", "-e", $baseCommit)
        if ($chk.Code -ne 0) { $haveBase = $false }
    }
    if ($haveBase) {
        foreach ($f in (Get-GitLines -GitArgs @("diff", "--name-only", $baseCommit, $head))) { $changed[$f] = $true }
    }
    if ($gitOk) {
        foreach ($f in (Get-GitLines -GitArgs @("diff", "--name-only", "HEAD"))) { $changed[$f] = $true }
        foreach ($f in (Get-GitLines -GitArgs @("diff", "--cached", "--name-only", "--diff-filter=A", "HEAD"))) { $changed[$f] = $true }
        foreach ($f in (Get-GitLines -GitArgs @("ls-files", "--others", "--exclude-standard"))) { $changed[$f] = $true }
    }

    # Hitung hash kondisi saat ini untuk setiap file berubah (DELETED bila hilang).
    $current = @{}
    foreach ($key in $changed.Keys) {
        $rel = $key -replace '\\', '/'
        $full = Join-Path $Root $rel
        if (Test-Path $full -PathType Leaf) {
            $h = Get-GitText -GitArgs @("hash-object", "--", $rel)
            $h = $h.Trim()
            if ($h -eq "" -or $h -notmatch '^[0-9a-f]{40}$') {
                $h = (Get-FileHash -Path $full -Algorithm SHA256).Hash.ToLower()
            }
            $current[$rel] = $h
        } else {
            $current[$rel] = "DELETED"
        }
    }

    # Hanya yang benar-benar baru sejak snapshot terakhir (path baru / hash beda).
    $newFiles = @()
    foreach ($rel in $current.Keys) {
        if (-not $consumed.ContainsKey($rel) -or $consumed[$rel] -ne $current[$rel]) {
            $newFiles += $rel
        }
    }

    $relevant = @($newFiles | Where-Object { -not (Test-Excluded $_) })

    # Versi: naik hanya kalau ada file kode/tooling berubah.
    $newVersion = $oldVersion
    $level = "none"
    $reasons = @()
    if ($relevant.Count -gt 0) {
        $newVersion = New-DeployVersion -OldVersion $oldVersion
        $level = "calver"
        $reasons += "$($relevant.Count) file berubah"
    }

    # Override: auto (hitung) | none (tetap) | vX.Y.Z (paksa untuk darurat).
    if ($Override -eq "none") {
        $reasons += "override: tetap"
        $newVersion = $oldVersion
        $level = "none"
    } elseif ($Override -and $Override -ne "auto") {
        if ($Override -match '^v?\d+\.\d+\.\d+$') {
            $lit = $Override; if ($lit -notmatch '^v') { $lit = "v$lit" }
            $reasons += "override manual: $lit"
            $newVersion = $lit
            $level = "calver"
        } else {
            throw "Nilai -Override tidak valid: '$Override' (pakai auto | none | vX.Y.Z)"
        }
    }

    if (-not $NoUpdate) {
        if ($level -ne "none") {
            Set-Content -Path $VersionPath -Value "$newVersion`n" -NoNewline:$false -Encoding Ascii
        } elseif (-not (Test-Path $VersionPath)) {
            Set-Content -Path $VersionPath -Value "$oldVersion`n" -Encoding Ascii
        }
        # Snapshot: commit HEAD + hash SEMUA file berubah saat ini (termasuk
        # yang excluded) supaya deploy berikutnya hanya melihat yang baru.
        $sb = New-Object System.Text.StringBuilder
        [void]$sb.AppendLine("commit=$head")
        foreach ($rel in ($current.Keys | Sort-Object)) {
            [void]$sb.AppendLine("$rel=$($current[$rel])")
        }
        Set-Content -Path $StatePath -Value $sb.ToString() -Encoding Ascii
    }

    return [pscustomobject]@{
        OldVersion = $oldVersion
        NewVersion = $newVersion
        Level      = $level
        Reason     = ($reasons -join "; ")
        NewFiles   = $relevant
        Head       = $head
    }
}

# Dipanggil langsung (.\version-bump.ps1 [-NoUpdate]) => cetak ringkasan.
if ($MyInvocation.InvocationName -ne ".") {
    $r = Invoke-VersionBump -Override $Override -VersionFile $VersionFile -StateFile $StateFile -NoUpdate:$NoUpdate
    Write-Host "level      : $($r.Level)"
    Write-Host "versi      : $($r.OldVersion) -> $($r.NewVersion)"
    Write-Host "alasan     : $($r.Reason)"
    if ($r.NewFiles.Count -gt 0) {
        Write-Host "file baru berubah:"
        foreach ($f in ($r.NewFiles | Sort-Object)) { Write-Host "  - $f" }
    } else {
        Write-Host "file baru berubah: (tidak ada)"
    }
    if ($NoUpdate) { Write-Host "(dry-run: VERSION/state tidak ditulis)" }
}
