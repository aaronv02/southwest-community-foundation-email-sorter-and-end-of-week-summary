<#
.SYNOPSIS
    Removes the Outlook tools from Claude Desktop.

.DESCRIPTION
    Removes only this one entry. Any other connectors in the file are left
    exactly as they were.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Disconnect-Claude.ps1
#>

[CmdletBinding()]
param(
    [string]$Name = 'swcf-outlook'
)

$ErrorActionPreference = 'Stop'

$configPath = Join-Path $env:APPDATA 'Claude\claude_desktop_config.json'

if (-not (Test-Path $configPath)) {
    Write-Host 'Claude Desktop has no config file - nothing to disconnect.'
    return
}

$raw = Get-Content $configPath -Raw
if ([string]::IsNullOrWhiteSpace($raw)) {
    Write-Host 'Claude config is empty - nothing to disconnect.'
    return
}

try {
    $config = $raw | ConvertFrom-Json
} catch {
    throw "The Claude config file is not valid JSON; leaving it alone: $configPath"
}

if (-not $config.PSObject.Properties.Name.Contains('mcpServers') -or
    -not $config.mcpServers.PSObject.Properties.Name.Contains($Name)) {
    Write-Host "No connector named '$Name' found - nothing to do."
    return
}

Copy-Item $configPath "$configPath.backup" -Force
$config.mcpServers.PSObject.Properties.Remove($Name)
$config | ConvertTo-Json -Depth 12 | Set-Content -Path $configPath -Encoding UTF8

$remaining = @($config.mcpServers.PSObject.Properties.Name)
Write-Host "Removed '$Name' from Claude Desktop." -ForegroundColor Green
if ($remaining.Count -gt 0) {
    Write-Host "Other connectors left in place: $($remaining -join ', ')"
}
Write-Host ''
Write-Host 'Quit Claude Desktop completely and reopen it for this to take effect.'
Write-Host ''
Write-Host 'Note: the weekly summary email is unaffected and still runs on Fridays.'
