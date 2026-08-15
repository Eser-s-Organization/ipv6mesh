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

function Wait-FileAvailable {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [int]$TimeoutSeconds = 15
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return
    }
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ($true) {
        $stream = $null
        try {
            $stream = [System.IO.File]::Open(
                $Path,
                [System.IO.FileMode]::Open,
                [System.IO.FileAccess]::ReadWrite,
                [System.IO.FileShare]::None
            )
            return
        } catch {
            if ((Get-Date) -ge $deadline) {
                throw "Timed out waiting for file to become available: $Path"
            }
            Start-Sleep -Milliseconds 250
        } finally {
            if ($null -ne $stream) {
                $stream.Dispose()
            }
        }
    }
}

$existingService = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -ne $existingService) {
    if ($existingService.Status -ne "Stopped") {
        Stop-Service -Name $ServiceName -Force -ErrorAction Stop
    }
    $existingService = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($null -ne $existingService) {
        $existingService.WaitForStatus("Stopped", [TimeSpan]::FromSeconds(15))
    }
}

New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
New-Item -ItemType Directory -Path $DataDirectory -Force | Out-Null
$servicePath = Join-Path $InstallDirectory "vpn-service.exe"
Wait-FileAvailable -Path $servicePath
foreach ($name in @("vpn-service.exe", "vpnctl.exe", "control-server.exe", "relay-agent.exe", "wireguard.dll", "wireguardnt-manifest.json", "wireguardnt-LICENSE.txt", "README.md")) {
    $source = Join-Path $package $name
    if (Test-Path -LiteralPath $source -PathType Leaf) {
        Copy-Item -LiteralPath $source -Destination (Join-Path $InstallDirectory $name) -Force
    }
}

if ($null -ne $existingService) {
    & sc.exe delete $ServiceName *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to delete existing Windows service $ServiceName"
    }
    $deleteDeadline = (Get-Date).AddSeconds(15)
    while ($null -ne (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
        if ((Get-Date) -ge $deleteDeadline) {
            throw "Timed out waiting for existing Windows service $ServiceName to be deleted"
        }
        Start-Sleep -Milliseconds 250
    }
}

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
