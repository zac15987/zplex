#!/usr/bin/env pwsh
<#
.SYNOPSIS
    One-click dev launch: rebuild the daemon, then start the app in dev mode.

.DESCRIPTION
    Rebuilds daemon/zplex-daemon.exe (via build.ps1 -DaemonOnly) so the binary
    that `npm run dev` auto-spawns contains the latest Go code, then launches the
    Electron app in dev mode (Vite dev server + Electron). This is the single
    command for the day-to-day UI test loop — only one terminal to manage.

    The frontend is NOT pre-built here: `npm run dev` serves it from source via
    Vite (hot reload), so only the daemon needs rebuilding.

    NOTE: the daemon auto-spawned by `npm run dev` runs with its output discarded
    (stdio: "ignore"), so daemon logs are NOT visible here. To debug the daemon,
    run it manually in a separate terminal instead:
        cd daemon; go run .
    The app detects the already-running daemon and skips spawning it.

.PARAMETER Install
    Run `npm install` before launching. Without this flag, install runs
    automatically only when app/node_modules is missing.

.EXAMPLE
    ./dev.ps1
    Rebuild the daemon and launch the app.

.EXAMPLE
    ./dev.ps1 -Install
    Refresh npm dependencies first, then rebuild and launch.
#>
[CmdletBinding()]
param(
    [switch]$Install
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot

# Rebuild the daemon so `npm run dev` auto-spawns fresh Go code.
# build.ps1 throws on failure, which aborts this script too.
& (Join-Path $root "build.ps1") -DaemonOnly

# Launch the app in dev mode (long-running; Ctrl+C or closing the window stops it).
Write-Host "`n==> Launching app (npm run dev)..." -ForegroundColor Cyan
Push-Location (Join-Path $root "app")
try {
    if ($Install -or -not (Test-Path "node_modules")) {
        Write-Host "==> Installing npm dependencies" -ForegroundColor Cyan
        npm install
        if ($LASTEXITCODE -ne 0) { throw "npm install failed (exit code $LASTEXITCODE)" }
    }
    npm run dev
}
finally {
    Pop-Location
}
