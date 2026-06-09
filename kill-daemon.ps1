#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Stop the zplex daemon listening on its port.

.DESCRIPTION
    During the dev/test loop the daemon is spawned detached (it survives Electron
    exit by design — see app/electron/main.ts), so closing the app leaves it
    running. This helper finds whatever process owns the daemon port and stops it,
    so you don't have to hunt it down in Task Manager.

    The port defaults to 17732 (the daemon default) and can be overridden to match
    a --port / ZPLEX_PORT you launched with.

.PARAMETER Port
    The daemon port to target. Defaults to 17732.

.EXAMPLE
    ./kill-daemon.ps1
    Stop the daemon on the default port 17732.

.EXAMPLE
    ./kill-daemon.ps1 -Port 17800
    Stop a daemon started on a custom port.
#>
[CmdletBinding()]
param(
    [int]$Port = 17732
)

$ErrorActionPreference = "Stop"

$owners = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue |
          Select-Object -ExpandProperty OwningProcess -Unique

if (-not $owners) {
    Write-Host "No daemon listening on port $Port — nothing to stop." -ForegroundColor DarkGray
    return
}

foreach ($ownerPid in $owners) {
    $proc = Get-Process -Id $ownerPid -ErrorAction SilentlyContinue
    $name = if ($proc) { $proc.ProcessName } else { "unknown" }
    Write-Host "==> Stopping daemon ($name, pid $ownerPid) on port $Port" -ForegroundColor Yellow
    Stop-Process -Id $ownerPid -Force -ErrorAction SilentlyContinue
}
