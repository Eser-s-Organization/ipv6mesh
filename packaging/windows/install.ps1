[CmdletBinding()]
param(
    [string]$PackageDirectory = "",
    [Parameter(Mandatory = $true)]
    [string]$ControlUrl,
    [string]$InstallDirectory = (Join-Path ${env:ProgramFiles} "IPv6Mesh"),
    [string]$DataDirectory = (Join-Path ${env:ProgramData} "IPv6Mesh"),
    [string]$ServiceName = "IPv6Mesh",
    [switch]$StartService
)

$ErrorActionPreference = "Stop"
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "install.ps1 must run from an elevated PowerShell window"
}

if ([string]::IsNullOrWhiteSpace($PackageDirectory)) {
    $PackageDirectory = $PSScriptRoot
}
if ([string]::IsNullOrWhiteSpace($PackageDirectory)) {
    throw "PackageDirectory could not be resolved; pass -PackageDirectory explicitly"
}

$package = (Resolve-Path -LiteralPath $PackageDirectory -ErrorAction Stop).Path
$serviceExecutable = Join-Path $package "vpn-service.exe"
$cliExecutable = Join-Path $package "vpnctl.exe"
$wireGuardDll = Join-Path $package "wireguard.dll"
foreach ($required in @($serviceExecutable, $cliExecutable, $wireGuardDll)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Package is missing $required. Build it with the official WireGuardNT wireguard.dll."
    }
}
if ([string]::IsNullOrWhiteSpace($ControlUrl)) {
    throw "ControlUrl is required, for example http://[2001:db8::1]:8080"
}

New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
New-Item -ItemType Directory -Path $DataDirectory -Force | Out-Null
foreach ($name in @("vpn-service.exe", "vpnctl.exe", "control-server.exe", "relay-agent.exe", "wireguard.dll", "wireguardnt-manifest.json", "wireguardnt-LICENSE.txt", "README.md")) {
    $source = Join-Path $package $name
    if (Test-Path -LiteralPath $source -PathType Leaf) {
        Copy-Item -LiteralPath $source -Destination (Join-Path $InstallDirectory $name) -Force
    }
}

& sc.exe query $ServiceName *> $null
if ($LASTEXITCODE -eq 0) {
    & sc.exe stop $ServiceName *> $null
    & sc.exe delete $ServiceName *> $null
    Start-Sleep -Milliseconds 500
}

$servicePath = Join-Path $InstallDirectory "vpn-service.exe"
$binaryPathName = '"' + $servicePath + '"'
New-Service `
    -Name $ServiceName `
    -BinaryPathName $binaryPathName `
    -DisplayName "IPv6Mesh VPN service" `
    -Description "IPv6-first virtual IPv4 mesh VPN service" `
    -StartupType Automatic `
    -ErrorAction Stop | Out-Null
& sc.exe description $ServiceName "IPv6-first virtual IPv4 mesh VPN service"
& sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/15000/none/0

[Environment]::SetEnvironmentVariable("IPV6MESH_CONTROL_URL", $ControlUrl, "Machine")
[Environment]::SetEnvironmentVariable("IPV6MESH_DATA_DIR", $DataDirectory, "Machine")

$firewallRule = "IPv6Mesh WireGuard UDP 51820"
if (-not (Get-NetFirewallRule -DisplayName $firewallRule -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -DisplayName $firewallRule -Direction Inbound -Protocol UDP -LocalPort 51820 -Action Allow -Profile Any | Out-Null
}

if ($StartService) {
    Start-Service -Name $ServiceName
}
Write-Host "IPv6Mesh installed in $InstallDirectory"
Write-Host "Control URL: $ControlUrl"
Write-Host "Data directory: $DataDirectory"
if (-not $StartService) {
    Write-Host "Start it with: Start-Service $ServiceName"
}
