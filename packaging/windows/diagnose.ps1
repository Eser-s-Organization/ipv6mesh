[CmdletBinding()]
param(
    [string]$ControlUrl = $env:IPV6MESH_CONTROL_URL,
    [string]$OutputPath = ""
)

$ErrorActionPreference = "SilentlyContinue"
$service = Get-Service -Name "IPv6Mesh" | Select-Object Name, Status, StartType
$addresses = Get-NetIPAddress -AddressFamily IPv6 | Select-Object IPAddress, InterfaceAlias, PrefixLength, SkipAsSource, AddressState, PrefixOrigin, SuffixOrigin
$adapters = Get-NetAdapter | Select-Object Name, InterfaceDescription, Status, LinkSpeed, MacAddress
$firewall = Get-NetFirewallRule -DisplayName "IPv6Mesh WireGuard UDP 51820" | Select-Object DisplayName, Enabled, Direction, Action, Profile
$routes = Get-NetRoute -AddressFamily IPv4 | Where-Object { $_.DestinationPrefix -like "*/32" } | Select-Object DestinationPrefix, NextHop, InterfaceAlias, RouteMetric, State

$control = [ordered]@{ Url = $ControlUrl; Reachable = $false; StatusCode = $null; Error = $null }
if (-not [string]::IsNullOrWhiteSpace($ControlUrl)) {
    try {
        $response = Invoke-WebRequest -Uri ($ControlUrl.TrimEnd("/") + "/healthz") -UseBasicParsing -TimeoutSec 10
        $control.Reachable = $response.StatusCode -eq 200
        $control.StatusCode = $response.StatusCode
    } catch {
        $control.Error = $_.Exception.Message
    }
}

$report = [ordered]@{
    GeneratedAt = [DateTime]::UtcNow.ToString("o")
    ComputerName = $env:COMPUTERNAME
    OS = (Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version, BuildNumber)
    Service = $service
    IPv6Addresses = $addresses
    Adapters = $adapters
    Firewall = $firewall
    HostRoutes = $routes
    ControlPlane = $control
}
$json = $report | ConvertTo-Json -Depth 8
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Join-Path ([IO.Path]::GetTempPath()) ("ipv6mesh-diagnose-{0}.json" -f $env:COMPUTERNAME)
}
Set-Content -LiteralPath $OutputPath -Value $json -Encoding UTF8
Write-Output $json
Write-Host "Diagnostic report: $OutputPath"
