[CmdletBinding()]
param(
    [string]$OutputDirectory = "",
    [string]$WireGuardDll = "",
    [string]$WireGuardLicense = "",
    [string]$Version = "0.1.0-dev",
    [string]$GoCommand = "go"
)

$ErrorActionPreference = "Stop"
$scriptRoot = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $scriptRoot "dist"
}
$repositoryRoot = (Resolve-Path (Join-Path $scriptRoot "..\..")).Path
$go = Get-Command $GoCommand -ErrorAction Stop
$output = (Resolve-Path $OutputDirectory -ErrorAction SilentlyContinue)
if ($null -eq $output) {
    New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
    $output = (Resolve-Path -LiteralPath $OutputDirectory -ErrorAction Stop)
}
$output = $output.ProviderPath

foreach ($name in @("vpn-service.exe", "vpnctl.exe", "control-server.exe", "relay-agent.exe")) {
    $path = Join-Path $output $name
    if (Test-Path -LiteralPath $path) {
        Remove-Item -LiteralPath $path -Force
    }
}

$env:CGO_ENABLED = "0"
$builds = @(
    @{ Package = ".\cmd\vpn-service"; Output = "vpn-service.exe" },
    @{ Package = ".\cmd\vpnctl"; Output = "vpnctl.exe" },
    @{ Package = ".\cmd\control-server"; Output = "control-server.exe" }
)

foreach ($build in $builds) {
    & $go.Source build -trimpath -ldflags "-s -w" -o (Join-Path $output $build.Output) (Join-Path $repositoryRoot $build.Package)
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed for $($build.Package)"
    }
}

$wireGuardTarget = Join-Path $output "wireguard.dll"
if ([string]::IsNullOrWhiteSpace($WireGuardDll)) {
    Write-Warning "wireguard.dll was not copied. A live Windows data-plane install requires the official WireGuardNT DLL."
} else {
    $wireGuardSource = (Resolve-Path -LiteralPath $WireGuardDll -ErrorAction Stop).Path
    if ([IO.Path]::GetFileName($wireGuardSource) -ne "wireguard.dll") {
        throw "WireGuard runtime must be named wireguard.dll"
    }
    Copy-Item -LiteralPath $wireGuardSource -Destination $wireGuardTarget -Force
    if (-not [string]::IsNullOrWhiteSpace($WireGuardLicense)) {
        Copy-Item -LiteralPath (Resolve-Path -LiteralPath $WireGuardLicense -ErrorAction Stop) -Destination (Join-Path $output "wireguardnt-LICENSE.txt") -Force
    } else {
        Write-Warning "WireGuardNT license was not copied; include the license from the exact official SDK archive in a release package."
    }
}

foreach ($file in @("install.ps1", "uninstall.ps1", "diagnose.ps1", "acceptance.ps1", "start-control-server.ps1", "README.md", "wireguardnt-manifest.json")) {
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot $file) -Destination (Join-Path $output $file) -Force
}

$versionFile = Join-Path $output "version.txt"
Set-Content -LiteralPath $versionFile -Value $Version -Encoding UTF8
Write-Host "IPv6Mesh package written to $output"
