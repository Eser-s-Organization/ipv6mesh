[CmdletBinding()]
param(
    [string]$InstallDirectory = (Join-Path ${env:ProgramFiles} "IPv6Mesh"),
    [string]$DataDirectory = (Join-Path ${env:ProgramData} "IPv6Mesh"),
    [string]$ServiceName = "IPv6Mesh",
    [switch]$RemoveData
)

$ErrorActionPreference = "Stop"
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "uninstall.ps1 must run from an elevated PowerShell window"
}

Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
& sc.exe delete $ServiceName *> $null
Remove-NetFirewallRule -DisplayName "IPv6Mesh WireGuard UDP 51820" -ErrorAction SilentlyContinue

if (Test-Path -LiteralPath $InstallDirectory) {
    Remove-Item -LiteralPath $InstallDirectory -Recurse -Force
}
if ($RemoveData -and (Test-Path -LiteralPath $DataDirectory)) {
    Remove-Item -LiteralPath $DataDirectory -Recurse -Force
}

[Environment]::SetEnvironmentVariable("IPV6MESH_CONTROL_URL", $null, "Machine")
[Environment]::SetEnvironmentVariable("IPV6MESH_DATA_DIR", $null, "Machine")
Write-Host "IPv6Mesh service and installed binaries removed. Data kept: $(-not $RemoveData)"
