[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$PeerVirtualIPv4,
    [string]$NetworkId = "",
    [int]$TcpPort = 0,
    [switch]$SkipConnect,
    [string]$VpnCtl = ""
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($VpnCtl)) {
    $candidate = Join-Path $PSScriptRoot "vpnctl.exe"
    if (Test-Path -LiteralPath $candidate) {
        $VpnCtl = $candidate
    } else {
        $VpnCtl = (Get-Command vpnctl.exe -ErrorAction Stop).Source
    }
}

if (-not $SkipConnect) {
    if ([string]::IsNullOrWhiteSpace($NetworkId)) {
        throw "NetworkId is required unless -SkipConnect is used"
    }
    & $VpnCtl connect --network $NetworkId
    if ($LASTEXITCODE -ne 0) {
        throw "vpnctl connect failed"
    }
}

Write-Host "Pinging peer virtual IPv4 $PeerVirtualIPv4"
& ping.exe -4 -n 4 $PeerVirtualIPv4
if ($LASTEXITCODE -ne 0) {
    throw "virtual IPv4 ping failed"
}

if ($TcpPort -gt 0) {
    $tcp = Test-NetConnection -ComputerName $PeerVirtualIPv4 -Port $TcpPort -InformationLevel Detailed
    if (-not $tcp.TcpTestSucceeded) {
        throw "TCP connection to $PeerVirtualIPv4`:$TcpPort failed"
    }
}
Write-Host "IPv6Mesh direct virtual IPv4 acceptance passed"
