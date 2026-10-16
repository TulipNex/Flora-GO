# version-bump.ps1 - penentu & penaik versi otomatis berbasis file yang berubah.
#
# Dipakai oleh deploy.ps1 sebelum build. Logika: bandingkan kondisi kerja
# saat ini (commit + worktree + untracked) dengan snapshot deploy terakhir
# (.last-deploy-state), lalu tentukan level dari file yang BENAR-BENAR BARU
# berubah sejak snapshot itu. Deploy ulang tanpa perubahan => level none
# (VERSION tidak disentuh, tidak ada bump ganda).
#
# Klasifikasi (level tertinggi menang):
#   major : penghapusan file command/tool (plugins/*.go, internal/ai/tools/**),
#           baris `go 1.x` di go.mod berubah, atau pesan commit mengandung
#           BREAKING / major!
#   minor : file .go BARU (untracked / staged-new / committed-A) di plugins/,
#           internal/, atau root; .env.example diubah; dependensi baru di go.mod
#   patch : perubahan kode/tooling lain (.go edit, .ps1, .sh, config, dsb)
#   none  : hanya file non-binary (README, data/, wabot-tmp/, .env, binary,
#           qr.png, VERSION/state itu sendiri, apps-script/, test-env.ps1)
#
# Cara pakai langsung (dry-run, tanpa menulis apa pun):
#   .\scripts\version-bump.ps1 -NoUpdate
# Dipakai dari script lain (dot-source, mengembalikan objek):
#   . .\scripts\version-bump.ps1
#   $v = Invoke-VersionBump -Override auto
param(
    [ValidateSet("auto", "major", "minor", "patch", "none")]
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

function Add-BumpedVersion {
    param([string]$Version, [string]$Level)
    $v = $Version -replace '^v', ''
    $parts = $v -split '\.'
    $major = [int]$parts[0]; $minor = [int]$parts[1]; $patch = [int]$parts[2]
    switch ($Level) {
        "major" { $major++; $minor = 0; $patch = 0 }
        "minor" { $minor++; $patch = 0 }
        "patch" { $patch++ }
    }
    return "v$major.$minor.$patch"
}

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
        [ValidateSet("auto", "major", "minor", "patch", "none")]
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
        foreach ($f in (Get-GitLines -GitArgs @("diff", "--cached", "--name-only", "--diff-filter=A", "HEAD"))) { $changed["STAGED_NEW:$f"] = $true }
        foreach ($f in (Get-GitLines -GitArgs @("ls-files", "--others", "--exclude-standard"))) { $changed["UNTRACKED:$f"] = $true }
    }

    # Normalisasi: pisahkan marker, hitung hash kondisi saat ini.
    $current = @{}
    $stagedNew = @()
    $untrackedGo = @()
    $deletedGo = @()
    foreach ($key in $changed.Keys) {
        $rel = $key
        $isNew = $false
        if ($rel -match '^STAGED_NEW:(.+)$') { $rel = $matches[1]; $isNew = $true }
        elseif ($rel -match '^UNTRACKED:(.+)$') {
            $rel = $matches[1]; $isNew = $true
            if ($rel -match '\.go$') { $untrackedGo += $rel }
        }
        $rel = $rel -replace '\\', '/'
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
            if ($rel -match '\.go$') { $deletedGo += $rel }
        }
        if ($isNew -and ($rel -match '\.go$')) { $stagedNew += $rel }
    }

    # File committed-A (baru masuk lewat commit sejak baseline) untuk sinyal minor.
    $committedNew = @()
    if ($haveBase) {
        foreach ($f in (Get-GitLines -GitArgs @("diff", "--name-only", "--diff-filter=A", $baseCommit, $head))) {
            $committedNew += (($f -replace '\\', '/'))
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

    # Tentukan level.
    $level = "none"
    $reasons = @()
    if ($relevant.Count -gt 0) {
        $level = "patch"
        $reasons += "$($relevant.Count) file kode/tooling berubah"

        # Penghapusan file command/tool = breaking => major.
        $majDel = @($deletedGo | Where-Object { $_ -match '^(plugins/[^/]+\.go|internal/ai/tools/.+)$' })
        if ($majDel.Count -gt 0) {
            $level = "major"
            $reasons += "hapus command/tool: $($majDel -join ', ')"
        }

        # go.mod: naik baris `go 1.x` => major; dependensi baru => minor.
        if ($relevant -contains "go.mod") {
            $gomodDiff = Get-GitText -GitArgs @("diff", $baseCommit, "--", "go.mod")
            $gomodDiff += "`n" + (Get-GitText -GitArgs @("diff", "--", "go.mod"))
            if ($gomodDiff -match '(?m)^[+-]go 1\.\d+') {
                $level = "major"
                $reasons += "versi Go di go.mod berubah"
            } elseif ($gomodDiff -match '(?m)^\+\s+[a-z0-9][a-z0-9./_-]*\s+v\d') {
                if ($level -ne "major") { $level = "minor" }
                $reasons += "dependensi baru di go.mod"
            }
        }

        # File .go baru di area fitur => minor.
        $newGo = @($relevant | Where-Object {
            $_ -match '\.go$' -and ($_ -match '^(plugins/|internal/|[^/]+\.go$)')
        })
        $explicitNew = @($stagedNew + $untrackedGo + $committedNew | ForEach-Object { $_ -replace '\\', '/' })
        $minorNew = @($newGo | Where-Object { $explicitNew -contains $_ })
        if ($minorNew.Count -gt 0 -and $level -eq "patch") {
            $level = "minor"
            $reasons += "file Go baru: $($minorNew -join ', ')"
        } elseif ($minorNew.Count -gt 0) {
            $reasons += "file Go baru: $($minorNew -join ', ')"
        }
        if (($relevant -contains ".env.example") -and $level -eq "patch") {
            $level = "minor"
            $reasons += ".env.example berubah (variabel env baru)"
        }

        # Pesan commit sejak baseline sebagai penegas (tak wajib, repo ini
        # historisnya berkomitmen generik sehingga default tetap heuristik path).
        if ($haveBase) {
            $msgs = Get-GitText -GitArgs @("log", "--format=%s%n%b", "$baseCommit..$head")
            if ($msgs -match 'BREAKING[ -]CHANGE|breaking|major!') {
                $level = "major"
                $reasons += "pesan commit menandai breaking"
            } elseif (($msgs -match '(?im)^feat(\(.+\))?[:!]') -and $level -eq "patch") {
                $level = "minor"
                $reasons += "pesan commit bertipe feat"
            }
        }

        if (-not $gitOk) {
            $level = "patch"
            $reasons = @("git tak tersedia - default patch (aman)")
        }
    }

    if ($Override -ne "auto") {
        $reasons += "override manual: $Override"
        $level = $Override
    }

    $newVersion = $oldVersion
    if ($level -ne "none") {
        $newVersion = Add-BumpedVersion -Version $oldVersion -Level $level
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
