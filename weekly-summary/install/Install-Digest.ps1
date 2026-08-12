<#
.SYNOPSIS
    Installs the weekly Outlook digest and schedules it for Friday afternoons.

.DESCRIPTION
    Copies digest.exe into the user's local app data and registers a Windows
    scheduled task.

    The task settings here are not boilerplate - they are the whole reason a
    laptop-hosted job is viable at all:

      -StartWhenAvailable      If the machine was off or asleep at 4pm Friday,
                               run at the next opportunity instead of skipping
                               the week silently. The program itself notices it
                               is late and reports the correct week.
      -WakeToRun               Wake from sleep to run. Does nothing if the
                               machine is fully powered off, which is why
                               StartWhenAvailable matters too.
      -AllowStartIfOnBatteries Windows refuses to start tasks on battery by
      -DontStopIfGoingOnBattery default. On a laptop that alone would stop this
                               working most weeks.
      -RestartCount            Retry on transient network failure.

    Runs as the current user while logged on, so no password is stored anywhere
    and the DPAPI-protected secret stays readable. The tradeoff is that the task
    will not fire while she is fully signed out - StartWhenAvailable covers that
    by running at her next sign-in.

.PARAMETER ExePath
    Path to digest.exe. Defaults to the file sitting next to this script.

.PARAMETER Time
    Time of day to run, 24-hour. Defaults to 16:00.

.PARAMETER Day
    Day of week to run. Defaults to Friday.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Install-Digest.ps1
#>

[CmdletBinding()]
param(
    [string]$ExePath = (Join-Path $PSScriptRoot 'digest.exe'),
    [string]$Time = '16:00',
    [ValidateSet('Monday','Tuesday','Wednesday','Thursday','Friday','Saturday','Sunday')]
    [string]$Day = 'Friday'
)

$ErrorActionPreference = 'Stop'
$TaskName = 'SWCF Weekly Digest'

function Write-Step($message) {
    Write-Host "==> $message" -ForegroundColor Cyan
}

if (-not (Test-Path $ExePath)) {
    throw "Could not find digest.exe at '$ExePath'. Pass -ExePath to point at it."
}

# --- Install the binary ----------------------------------------------------

$InstallDir = Join-Path $env:LOCALAPPDATA 'SWCFDigest'
Write-Step "Installing to $InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$TargetExe = Join-Path $InstallDir 'digest.exe'
# Stop a running instance so the copy cannot fail with a file lock.
Get-Process -Name 'digest' -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq $TargetExe } |
    Stop-Process -Force -ErrorAction SilentlyContinue

Copy-Item -Path $ExePath -Destination $TargetExe -Force
Write-Host "    digest.exe installed"

# --- Configure, if not already ---------------------------------------------

$ConfigPath = Join-Path $env:APPDATA 'SWCFDigest\config.json'
if (-not (Test-Path $ConfigPath)) {
    Write-Step 'No configuration found - starting setup'
    Write-Host '    You will need the tenant ID, client ID, and client secret.'
    Write-Host ''
    & $TargetExe --setup
    if ($LASTEXITCODE -ne 0) {
        throw 'Setup did not complete. Fix the errors above and run this script again.'
    }
} else {
    Write-Step 'Existing configuration found - verifying access'
    & $TargetExe --check
    if ($LASTEXITCODE -ne 0) {
        throw 'Could not reach the mailbox. Run "digest.exe --setup" to correct the settings.'
    }
}

# --- Register the scheduled task -------------------------------------------

Write-Step "Scheduling for $Day at $Time"

if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Write-Host '    Removing previous schedule'
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}

$action = New-ScheduledTaskAction -Execute $TargetExe -WorkingDirectory $InstallDir

$trigger = New-ScheduledTaskTrigger -Weekly -DaysOfWeek $Day -At $Time

$settings = New-ScheduledTaskSettingsSet `
    -StartWhenAvailable `
    -WakeToRun `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -RunOnlyIfNetworkAvailable `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 15) `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 30) `
    -MultipleInstances IgnoreNew

# Interactive: runs as this user when signed in, so no password is stored and
# the DPAPI secret stays decryptable.
$principal = New-ScheduledTaskPrincipal `
    -UserId "$env:USERDOMAIN\$env:USERNAME" `
    -LogonType Interactive `
    -RunLevel Limited

Register-ScheduledTask `
    -TaskName $TaskName `
    -Action $action `
    -Trigger $trigger `
    -Settings $settings `
    -Principal $principal `
    -Description 'Emails a weekly summary of Outlook mail and calendar activity.' | Out-Null

Write-Host "    Scheduled task '$TaskName' registered"

# --- Done -------------------------------------------------------------------

$next = (Get-ScheduledTask -TaskName $TaskName | Get-ScheduledTaskInfo).NextRunTime

Write-Host ''
Write-Host 'Installed.' -ForegroundColor Green
Write-Host "  Program:   $TargetExe"
Write-Host "  Settings:  $ConfigPath"
Write-Host "  Log:       $(Join-Path $env:APPDATA 'SWCFDigest\digest.log')"
Write-Host "  Next run:  $next"
Write-Host ''
Write-Host 'To see what the email will look like right now, run:'
Write-Host "  & '$TargetExe' --preview `$env:USERPROFILE\Desktop\digest-preview.html"
Write-Host ''
Write-Host 'To send one immediately:'
Write-Host "  & '$TargetExe' --force"
