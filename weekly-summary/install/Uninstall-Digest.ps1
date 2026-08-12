<#
.SYNOPSIS
    Removes the weekly digest scheduled task and program.

.DESCRIPTION
    Settings and logs are left in place by default, so reinstalling does not
    mean re-entering credentials. Pass -Purge to remove those too.

.PARAMETER Purge
    Also delete the configuration, saved state, and logs.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Uninstall-Digest.ps1
#>

[CmdletBinding()]
param(
    [switch]$Purge
)

$ErrorActionPreference = 'Stop'
$TaskName = 'SWCF Weekly Digest'

if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    Write-Host "Removed scheduled task '$TaskName'"
} else {
    Write-Host "No scheduled task named '$TaskName' found"
}

$InstallDir = Join-Path $env:LOCALAPPDATA 'SWCFDigest'
$TargetExe = Join-Path $InstallDir 'digest.exe'

Get-Process -Name 'digest' -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq $TargetExe } |
    Stop-Process -Force -ErrorAction SilentlyContinue

if (Test-Path $InstallDir) {
    Remove-Item -Recurse -Force $InstallDir
    Write-Host "Removed $InstallDir"
}

$DataDir = Join-Path $env:APPDATA 'SWCFDigest'
if ($Purge) {
    if (Test-Path $DataDir) {
        Remove-Item -Recurse -Force $DataDir
        Write-Host "Removed settings and logs from $DataDir"
    }
    Write-Host ''
    Write-Host 'Note: the Entra app registration and its client secret still exist.'
    Write-Host 'Delete the app registration in Entra to fully revoke mailbox access.'
} elseif (Test-Path $DataDir) {
    Write-Host ''
    Write-Host "Settings kept at $DataDir (use -Purge to remove them)."
}

Write-Host ''
Write-Host 'Uninstalled.' -ForegroundColor Green
