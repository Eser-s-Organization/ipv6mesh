[CmdletBinding()]
param(
    [string]$ControlServer = "",
    [string]$ListenAddress = "[::]:8080",
    [string]$BootstrapToken = $env:CONTROL_BOOTSTRAP_TOKEN,
    [string]$SessionTtl = "24h",
    [string]$InviteTtl = "24h",
    [switch]$OpenFirewall
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($ControlServer)) {
    $candidate = Join-Path $PSScriptRoot "control-server.exe"
    if (Test-Path -LiteralPath $candidate) {
        $ControlServer = $candidate
    } else {
        $ControlServer = (Get-Command control-server.exe -ErrorAction Stop).Source
    }
}
if ([string]::IsNullOrWhiteSpace($BootstrapToken)) {
    $BootstrapToken = Read-Host "CONTROL_BOOTSTRAP_TOKEN"
}
if ([string]::IsNullOrWhiteSpace($BootstrapToken)) {
    throw "BootstrapToken is required"
}

if ($OpenFirewall) {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "-OpenFirewall requires an elevated PowerShell window"
    }
    $ruleName = "IPv6Mesh Control Plane TCP 8080"
    if (-not (Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Protocol TCP -LocalPort 8080 -Action Allow -Profile Any | Out-Null
    }
}

$env:CONTROL_LISTEN_ADDRESS = $ListenAddress
$env:CONTROL_BOOTSTRAP_TOKEN = $BootstrapToken
$env:CONTROL_SESSION_TTL = $SessionTtl
$env:CONTROL_INVITE_TTL = $InviteTtl
$env:CONTROL_REPOSITORY_MODE = "memory"
Write-Host "Starting IPv6Mesh control plane on $ListenAddress (memory repository)"
& $ControlServer
exit $LASTEXITCODE
