#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Build both zplex components in one shot: the Go daemon and the Electron frontend.

.DESCRIPTION
    Compiles the Go daemon to daemon/zplex-daemon.exe and builds the frontend
    (tsc + vite) into app/dist. Paths resolve relative to this script, so it can
    be run from any working directory.

    Building the daemon here keeps daemon/zplex-daemon.exe fresh, which is the
    binary `npm run dev` auto-spawns — so after a build you can launch the whole
    stack with a single `cd app; npm run dev`.

.PARAMETER Install
    Run `npm install` in app/ before building. Without this flag, install runs
    automatically only when app/node_modules is missing.

.PARAMETER DaemonOnly
    Build only the Go daemon.

.PARAMETER AppOnly
    Build only the Electron frontend.

.EXAMPLE
    ./build.ps1
    Build both components.

.EXAMPLE
    ./build.ps1 -Install
    Force a fresh npm install, then build both.

.EXAMPLE
    ./build.ps1 -DaemonOnly
    Rebuild just the daemon (e.g. after editing Go code).
#>
[CmdletBinding()]
param(
    [switch]$Install,
    [switch]$DaemonOnly,
    [switch]$AppOnly
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot

function Write-Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg" -ForegroundColor Green }

# --- Toolchain checks -------------------------------------------------------
if (-not $AppOnly -and -not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "go was not found in PATH. Install Go or use -AppOnly."
}
if (-not $DaemonOnly -and -not (Get-Command npm -ErrorAction SilentlyContinue)) {
    throw "npm was not found in PATH. Install Node.js or use -DaemonOnly."
}

$started = Get-Date

# --- Go daemon --------------------------------------------------------------
if (-not $AppOnly) {
    Write-Step "Building Go daemon -> daemon/zplex-daemon.exe"
    Push-Location (Join-Path $root "daemon")
    try {
        go build -o zplex-daemon.exe .
        if ($LASTEXITCODE -ne 0) { throw "go build failed (exit code $LASTEXITCODE)" }
        Write-Ok "daemon built"
    }
    finally {
        Pop-Location
    }
}

# --- Electron frontend ------------------------------------------------------
if (-not $DaemonOnly) {
    Write-Step "Building Electron frontend -> app/dist"
    Push-Location (Join-Path $root "app")
    try {
        if ($Install -or -not (Test-Path "node_modules")) {
            Write-Step "Installing npm dependencies"
            npm install
            if ($LASTEXITCODE -ne 0) { throw "npm install failed (exit code $LASTEXITCODE)" }
        }
        npm run build
        if ($LASTEXITCODE -ne 0) { throw "npm run build failed (exit code $LASTEXITCODE)" }
        Write-Ok "frontend built"
    }
    finally {
        Pop-Location
    }
}

# --- Summary ----------------------------------------------------------------
$elapsed = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)
Write-Host "`n[OK] Build complete in ${elapsed}s" -ForegroundColor Green
Write-Host "     Launch the app with:  cd app; npm run dev" -ForegroundColor DarkGray
