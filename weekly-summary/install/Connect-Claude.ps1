<#
.SYNOPSIS
    Connects the Outlook tools to Claude Desktop.

.DESCRIPTION
    Claude Desktop learns about local tools from a JSON file. Editing that by
    hand is not something to ask a non-technical user to do, so this writes it.

    It MERGES rather than overwrites. If other connectors are already set up,
    they are preserved, and the existing file is backed up first. Overwriting
    someone's config to add one entry would be a rude way to break their other
    tools.

.PARAMETER ExePath
    Path to digest.exe. Defaults to the installed copy.

.PARAMETER Name
    Key used in the config. Change only if it collides with something.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Connect-Claude.ps1
#>

[CmdletBinding()]
param(
    [string]$ExePath = (Join-Path $env:LOCALAPPDATA 'SWCFDigest\digest.exe'),
    [string]$Name = 'swcf-outlook'
)

$ErrorActionPreference = 'Stop'

function Write-Step($m) { Write-Host "==> $m" -ForegroundColor Cyan }

# --- Preconditions ---------------------------------------------------------

if (-not (Test-Path $ExePath)) {
    throw @"
Could not find digest.exe at:
  $ExePath

Install the Weekly Summary first (double-click "Install Weekly Summary.cmd"),
then run this again.
"@
}

$configDir = Join-Path $env:APPDATA 'Claude'
if (-not (Test-Path $configDir)) {
    throw @"
Claude Desktop does not appear to be installed for this user.
Expected to find: $configDir

Install Claude Desktop and sign in once, then run this again.
"@
}

# The mailbox settings must already exist, or Claude will connect to a server
# that immediately fails. Better to catch it here than to leave her with a tool
# that errors on every use.
$digestConfig = Join-Path $env:APPDATA 'SWCFDigest\config.json'
if (-not (Test-Path $digestConfig)) {
    throw @"
The Outlook connection is not set up yet.
Run "Install Weekly Summary.cmd" first - it asks for the mailbox details.
"@
}

Write-Step 'Checking mailbox access'
& $ExePath --check
if ($LASTEXITCODE -ne 0) {
    throw 'Could not reach the mailbox. Fix that before connecting Claude.'
}

# --- Merge into the config -------------------------------------------------

$configPath = Join-Path $configDir 'claude_desktop_config.json'
Write-Step "Updating $configPath"

$config = $null
if (Test-Path $configPath) {
    $backup = "$configPath.backup"
    Copy-Item $configPath $backup -Force
    Write-Host "    Backed up existing config to $backup"

    $raw = Get-Content $configPath -Raw
    if (-not [string]::IsNullOrWhiteSpace($raw)) {
        try {
            $config = $raw | ConvertFrom-Json
        } catch {
            throw @"
The existing Claude config file is not valid JSON:
  $configPath

It has been backed up to $backup. Fix or delete the original, then run this
again. Refusing to overwrite it in case it contains other connectors.
"@
        }
    }
}

if ($null -eq $config) {
    $config = [PSCustomObject]@{}
}

# PowerShell 5.1 gives PSCustomObject from ConvertFrom-Json, so members are
# added rather than assigned like a hashtable.
if (-not $config.PSObject.Properties.Name.Contains('mcpServers')) {
    $config | Add-Member -MemberType NoteProperty -Name 'mcpServers' -Value ([PSCustomObject]@{})
}

$existingNames = @($config.mcpServers.PSObject.Properties.Name)
$replacing = $existingNames -contains $Name

$entry = [PSCustomObject]@{
    command = $ExePath
    args    = @('--mcp')
}

if ($replacing) {
    $config.mcpServers.$Name = $entry
} else {
    $config.mcpServers | Add-Member -MemberType NoteProperty -Name $Name -Value $entry
}

$others = @($existingNames | Where-Object { $_ -ne $Name })
if ($others.Count -gt 0) {
    Write-Host "    Keeping $($others.Count) other connector(s): $($others -join ', ')"
}

$config | ConvertTo-Json -Depth 12 | Set-Content -Path $configPath -Encoding UTF8

# --- Done ------------------------------------------------------------------

Write-Host ''
Write-Host 'Connected.' -ForegroundColor Green
Write-Host ''
Write-Host '  IMPORTANT: quit Claude Desktop completely and reopen it.'
Write-Host '  (Closing the window is not enough - right-click the icon in the'
Write-Host '   system tray near the clock and choose Quit.)'
Write-Host ''
Write-Host '  Then try asking Claude:'
Write-Host '    "what emails am I forgetting?"'
Write-Host '    "how was my week?"'
Write-Host '    "sort my emails"'
Write-Host ''
