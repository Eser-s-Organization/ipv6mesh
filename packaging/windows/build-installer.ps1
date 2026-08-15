[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$WireGuardDll,
    [Parameter(Mandatory = $true)]
    [string]$WireGuardLicense,
    [string]$OutputPath = (Join-Path $PSScriptRoot "dist\ipv6mesh-installer.exe"),
    [string]$Version = "0.1.0-dev",
    [string]$GoCommand = "go"
)

$ErrorActionPreference = "Stop"
$scriptRoot = $PSScriptRoot
$repositoryRoot = (Resolve-Path (Join-Path $scriptRoot "..\..")).Path
$payloadDirectory = Join-Path $scriptRoot "dist\installer-payload"
$installerSource = Join-Path $repositoryRoot "cmd\ipv6mesh-installer"
$payloadZip = Join-Path $installerSource "payload.zip"
$payloadEmbedSource = Join-Path $installerSource "payload_embed_windows.go"
$output = [IO.Path]::GetFullPath($OutputPath)
$outputParent = Split-Path -Parent $output

$go = Get-Command $GoCommand -ErrorAction Stop
New-Item -ItemType Directory -Path $outputParent -Force | Out-Null
if (Test-Path -LiteralPath $payloadDirectory) {
    Remove-Item -LiteralPath $payloadDirectory -Recurse -Force
}
if (Test-Path -LiteralPath $payloadZip) {
    Remove-Item -LiteralPath $payloadZip -Force
}
if (Test-Path -LiteralPath $payloadEmbedSource) {
    Remove-Item -LiteralPath $payloadEmbedSource -Force
}

try {
    & (Join-Path $scriptRoot "build.ps1") `
        -OutputDirectory $payloadDirectory `
        -WireGuardDll $WireGuardDll `
        -WireGuardLicense $WireGuardLicense `
        -Version $Version `
        -GoCommand $GoCommand
    if ($LASTEXITCODE -ne 0) {
        throw "Windows package build failed"
    }

    Compress-Archive `
        -Path (Join-Path $payloadDirectory "*") `
        -DestinationPath $payloadZip `
        -CompressionLevel Optimal

    @'
//go:build windows && installerpayload

package main

import _ "embed"

//go:embed payload.zip
var embeddedPayload []byte
'@ | Set-Content -LiteralPath $payloadEmbedSource -Encoding utf8

    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    if (Test-Path -LiteralPath $output) {
        Remove-Item -LiteralPath $output -Force
    }
    & $go.Source build `
        -tags installerpayload `
        -trimpath `
        -ldflags "-H=windowsgui -s -w -X main.version=$Version" `
        -o $output `
        (Join-Path $repositoryRoot "cmd\ipv6mesh-installer")
    if ($LASTEXITCODE -ne 0) {
        throw "Installer build failed"
    }

    $hash = (Get-FileHash -LiteralPath $output -Algorithm SHA256).Hash
    Set-Content -LiteralPath ($output + ".sha256") -Value "$hash  $(Split-Path -Leaf $output)" -Encoding ascii
    $size = (Get-Item -LiteralPath $output).Length
    Write-Host "Installer written to $output ($size bytes)"
    Write-Host "SHA-256: $hash"
}
finally {
    if (Test-Path -LiteralPath $payloadZip) {
        Remove-Item -LiteralPath $payloadZip -Force
    }
    if (Test-Path -LiteralPath $payloadEmbedSource) {
        Remove-Item -LiteralPath $payloadEmbedSource -Force
    }
    if (Test-Path -LiteralPath $payloadDirectory) {
        Remove-Item -LiteralPath $payloadDirectory -Recurse -Force
    }
}
