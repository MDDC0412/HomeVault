# =============================================================================
# homevault cross-compile script
# Windows PowerShell 5.1+ or PowerShell 7+
#
#   powershell -ExecutionPolicy Bypass -File .\build.ps1
#   pwsh -File .\build.ps1
#   pwsh -File .\build.ps1 -Targets linux/amd64,linux/arm64
#
# Artifacts are written to dist\ :
#   homevault-windows-amd64.exe
#   homevault-linux-amd64
#   homevault-linux-arm64
#   homevault-linux-armv7
#   homevault-darwin-amd64
#   homevault-darwin-arm64
#
# Notes:
#   * CGO_ENABLED=0      static binary, no libc dependency (works in alpine/distroless).
#   * -trimpath          strips build-machine paths from the binary.
#   * -ldflags "-s -w"   strips symbol table and DWARF info (~25% smaller).
#   * This module has NO third-party dependencies, so this script performs no
#     network access at all (no go mod download / go get / go install).
#   * The version string is read from internal/server/server.go purely for
#     display. It is NOT injected via -ldflags: the main package has no
#     `version` variable, so `-X main.version=...` would be silently ignored.
#
# This file is intentionally ASCII-only so that Windows PowerShell 5.1 (which
# decodes .ps1 files as ANSI unless they carry a BOM) can parse it correctly.
# Comments and help text live in docs\DEPLOY.md and docs\CONFIG.md.
# =============================================================================

[CmdletBinding()]
param(
    # Build only these targets, e.g. -Targets linux/amd64,linux/arm64
    # Valid: windows/amd64, linux/amd64, linux/arm64, linux/arm, darwin/amd64, darwin/arm64
    [string[]]$Targets = @(),
    # Output directory, relative to this script.
    [string]$OutDir = 'dist'
)

Set-StrictMode -Version Latest

$root = $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }

$targetFilter = @($Targets)
$results = @()
$failed = @()
$exitCode = 0

Push-Location $root
try {
    # -------------------------------------------------------------------------
    # 0. locate go.exe
    # -------------------------------------------------------------------------
    $goExe = $null

    $envGoExe = [string]$env:GOEXE
    if ($envGoExe -and (Test-Path -LiteralPath $envGoExe)) {
        $goExe = $envGoExe
    }
    if (-not $goExe) {
        $cmd = Get-Command go -ErrorAction SilentlyContinue
        if ($cmd) { $goExe = $cmd.Source }
    }
    if (-not $goExe) {
        foreach ($cand in @(
            'C:\Program Files\Go\bin\go.exe',
            'C:\Program Files (x86)\Go\bin\go.exe',
            "$env:LOCALAPPDATA\Programs\Go\bin\go.exe"
        )) {
            if (Test-Path -LiteralPath $cand) { $goExe = $cand; break }
        }
    }
    if (-not $goExe) {
        throw 'go.exe not found. Install Go 1.24+ and put it on PATH, or set $env:GOEXE to its full path.'
    }

    Write-Host ''
    Write-Host '  homevault cross-compile' -ForegroundColor Cyan
    Write-Host '  ---------------------------------------------'

    # -------------------------------------------------------------------------
    # 1. read version / module name (display only)
    # -------------------------------------------------------------------------
    $version = '0.0.0'
    $serverGo = Join-Path $root 'internal\server\server.go'
    if (Test-Path -LiteralPath $serverGo) {
        $m = @(Select-String -LiteralPath $serverGo -Pattern 'Version\s*=\s*"([^"]+)"')
        if ($m.Count -gt 0) { $version = $m[0].Matches[0].Groups[1].Value }
    }

    $moduleName = 'homevault'
    $gomod = Join-Path $root 'go.mod'
    if (Test-Path -LiteralPath $gomod) {
        $mm = @(Select-String -LiteralPath $gomod -Pattern '^module\s+(\S+)')
        if ($mm.Count -gt 0) { $moduleName = $mm[0].Matches[0].Groups[1].Value }
    }

    Write-Host ("  Go      : {0}" -f (& $goExe version))
    Write-Host ("  version : {0}  (from internal\server\server.go)" -f $version)
    Write-Host ("  module  : {0}" -f $moduleName)
    Write-Host ''

    # -------------------------------------------------------------------------
    # 2. target matrix
    # -------------------------------------------------------------------------
    $allTargets = @(
        [pscustomobject]@{ GOOS = 'windows'; GOARCH = 'amd64'; GOARM = '';  Out = 'homevault-windows-amd64.exe' }
        [pscustomobject]@{ GOOS = 'linux';   GOARCH = 'amd64'; GOARM = '';  Out = 'homevault-linux-amd64' }
        [pscustomobject]@{ GOOS = 'linux';   GOARCH = 'arm64'; GOARM = '';  Out = 'homevault-linux-arm64' }
        [pscustomobject]@{ GOOS = 'linux';   GOARCH = 'arm';   GOARM = '7'; Out = 'homevault-linux-armv7' }
        [pscustomobject]@{ GOOS = 'darwin';  GOARCH = 'amd64'; GOARM = '';  Out = 'homevault-darwin-amd64' }
        [pscustomobject]@{ GOOS = 'darwin';  GOARCH = 'arm64'; GOARM = '';  Out = 'homevault-darwin-arm64' }
    )

    if ($targetFilter.Count -gt 0) {
        # Accept both `-Targets linux/amd64,linux/arm64` (array, from pwsh/-Command)
        # and `-File build.ps1 -Targets linux/amd64,linux/arm64` (a single comma-joined
        # string, because -File passes positional arguments verbatim).
        $wanted = @{}
        foreach ($t in $targetFilter) {
            foreach ($piece in ($t -split ',')) {
                $key = $piece.Trim().ToLower()
                if ($key) { $wanted[$key] = $true }
            }
        }
        $allTargets = @($allTargets | Where-Object { $wanted.ContainsKey("$($_.GOOS)/$($_.GOARCH)") })
        if ($allTargets.Count -eq 0) {
            throw 'No matching target. Valid: windows/amd64, linux/amd64, linux/arm64, linux/arm, darwin/amd64, darwin/arm64'
        }
    }

    # -------------------------------------------------------------------------
    # 3. output directory
    # -------------------------------------------------------------------------
    $outAbs = Join-Path $root $OutDir
    if (-not (Test-Path -LiteralPath $outAbs)) {
        New-Item -ItemType Directory -Path $outAbs -Force | Out-Null
    }

    # Cross compilation never needs cgo; disabling it also removes the need for
    # a local C compiler and makes the artifact static.
    $env:CGO_ENABLED = '0'

    # -------------------------------------------------------------------------
    # 4. build each target
    # -------------------------------------------------------------------------
    foreach ($t in $allTargets) {
        $env:GOOS = $t.GOOS
        $env:GOARCH = $t.GOARCH

        # GOARM only applies to GOARCH=arm. It must be cleared for every other
        # architecture, otherwise the toolchain rejects the combination.
        if ($t.GOARM) {
            $env:GOARM = $t.GOARM
        } else {
            Remove-Item -Path 'Env:GOARM' -ErrorAction SilentlyContinue
        }

        $label = if ($t.GOARM) { "$($t.GOOS)/$($t.GOARCH)v$($t.GOARM)" } else { "$($t.GOOS)/$($t.GOARCH)" }
        $outFile = Join-Path $outAbs $t.Out

        Write-Host ("  [{0,-16}] building ..." -f $label) -NoNewline

        $sw = [System.Diagnostics.Stopwatch]::StartNew()
        $output = & $goExe build -trimpath -ldflags '-s -w' -o $outFile . 2>&1
        $code = $LASTEXITCODE
        $sw.Stop()

        if ($code -ne 0) {
            Write-Host ' FAILED' -ForegroundColor Red
            Write-Host ([string]::Join("`n", @($output))) -ForegroundColor Red
            $failed += $label
            continue
        }

        $fi = Get-Item -LiteralPath $outFile
        $sizeMb = [math]::Round($fi.Length / 1MB, 2)
        Write-Host (" OK  {0,10:N0} bytes / {1,7} MB  in {2:N1}s" -f $fi.Length, $sizeMb, $sw.Elapsed.TotalSeconds) -ForegroundColor Green

        $results += [pscustomobject]@{
            Target = $label
            Path   = $fi.FullName
            Bytes  = $fi.Length
            MB     = $sizeMb
        }
    }

    # -------------------------------------------------------------------------
    # 5. summary
    # -------------------------------------------------------------------------
    Write-Host ''
    Write-Host '  ---------------------------------------------'
    if ($results.Count -gt 0) {
        $totalMb = [math]::Round((($results | Measure-Object -Property Bytes -Sum).Sum) / 1MB, 2)
        Write-Host ("  {0} artifact(s), {1} MB total" -f $results.Count, $totalMb) -ForegroundColor Cyan
        foreach ($r in $results) {
            Write-Host ("    {0,-18} {1,10:N0} bytes  {2}" -f $r.Target, $r.Bytes, $r.Path)
        }
        Write-Host ''
        Write-Host ("  output dir: {0}" -f $outAbs)
    }

    if ($failed.Count -gt 0) {
        Write-Host ("  failed: {0}" -f ($failed -join ', ')) -ForegroundColor Red
        $exitCode = 1
    }
    else {
        Write-Host ''
        Write-Host '  next steps:' -ForegroundColor Cyan
        Write-Host '    1) smoke test : .\dist\homevault-windows-amd64.exe serve'
        Write-Host '    2) deployment : docs\DEPLOY.md'
        Write-Host '    3) first login: http://127.0.0.1:18090/admin/   (admin / admin, must be changed)'
    }
    Write-Host ''
}
catch {
    Write-Host ''
    Write-Host ("  build script error: {0}" -f $_.Exception.Message) -ForegroundColor Red
    $exitCode = 1
}
finally {
    # Clear the temporary environment variables so the caller's session is clean.
    foreach ($v in @('GOOS', 'GOARCH', 'GOARM', 'CGO_ENABLED')) {
        Remove-Item -Path "Env:$v" -ErrorAction SilentlyContinue
    }
    Pop-Location
}

exit $exitCode
