[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$PackageDirectory,
    [string]$ControlUrl = "",
    [string]$Invite = "",
    [string]$DeviceName = "",
    [string]$Network = "",
    [string]$InstallDirectory = (Join-Path $env:ProgramFiles "IPv6Mesh"),
    [string]$DataDirectory = (Join-Path $env:ProgramData "IPv6Mesh"),
    [string]$ServiceName = "IPv6Mesh",
    [string]$Version = "dev"
)

$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()

$script:logLines = New-Object 'System.Collections.Generic.List[string]'
$script:controlProcess = $null
$script:controlEventSources = @()
$script:form = $null
$script:statusLabel = $null
$script:logBox = $null
$script:controlUrlBox = $null
$script:ipv6AddressBox = $null
$script:memberHostIPv6Box = $null
$script:memberNameLabel = $null
$script:nodeStatusLabel = $null
$script:hostVirtualIPv4Label = $null
$script:memberVirtualIPv4Label = $null
$script:welcomePanel = $null
$script:hostPanel = $null
$script:memberPanel = $null
$script:diagnosticsPanel = $null
$script:activePage = "Welcome"
$script:hostStartButton = $null
$script:memberJoinButton = $null
$script:backButtons = @()
$script:controlUrl = ""
$script:adminToken = ""
$script:activeNetworkId = ""
$script:startedControlPlane = $false
$script:startedNodeService = $false
$script:cleanupStarted = $false
$script:primaryBusy = $false
$script:updatingEndpoint = $false
$script:statusRefreshTimer = $null
$script:statusRefreshInProgress = $false
$script:hasStatusRefreshResult = $false
$script:lastStatusRefreshSucceeded = $false
$script:lastStatusFingerprint = ""

function Redact-Secret {
    param([AllowNull()][string]$Value)
    if ($null -eq $Value) { return "" }
    $result = $Value
    if (![string]::IsNullOrWhiteSpace($script:adminToken)) {
        $result = $result.Replace($script:adminToken, "[redacted]")
    }
    return $result
}

function Add-UiLog {
    param([Parameter(Mandatory = $true)][string]$Message, [string]$Level = "信息")
    $safeMessage = Redact-Secret $Message
    $line = "[{0}] [{1}] {2}" -f (Get-Date).ToString("HH:mm:ss"), $Level, $safeMessage
    [void]$script:logLines.Add($line)
    if ($null -eq $script:logBox -or $script:logBox.IsDisposed) { return }
    $update = [Action]{
        if ($script:logBox.IsDisposed) { return }
        $script:logBox.AppendText($line + [Environment]::NewLine)
        $script:logBox.SelectionStart = $script:logBox.TextLength
        $script:logBox.ScrollToCaret()
    }
    if ($script:logBox.InvokeRequired) {
        [void]$script:logBox.BeginInvoke($update)
    } else {
        $update.Invoke()
    }
}

function Get-StatusLogDecision {
    param(
        [bool]$Automatic,
        [bool]$Succeeded,
        [AllowEmptyString()][string]$Fingerprint,
        [bool]$HasPrevious,
        [bool]$PreviousSucceeded,
        [AllowEmptyString()][string]$PreviousFingerprint
    )
    if (!$Automatic) { return "Manual" }
    if (!$HasPrevious) {
        if ($Succeeded) { return "Changed" }
        return "Failed"
    }
    if (!$Succeeded) {
        if ($PreviousSucceeded) { return "Failed" }
        return "None"
    }
    if (!$PreviousSucceeded) { return "Recovered" }
    if ($Fingerprint -ne $PreviousFingerprint) { return "Changed" }
    return "None"
}

function Get-SplitLayoutDecision {
    param(
        [int]$AvailableHeight,
        [int]$SplitterWidth,
        [int]$CurrentDistance = -1
    )
    $height = [Math]::Max(0, $AvailableHeight)
    $splitter = [Math]::Max(0, $SplitterWidth)
    $usable = [Math]::Max(0, $height - $splitter)
    if ($usable -eq 0) {
        return [pscustomobject]@{ UpperMinimum = 0; LowerMinimum = 0; Distance = 0 }
    }

    if ($usable -ge 450) {
        $upperMinimum = 250
        $lowerMinimum = 200
    } else {
        $upperMinimum = [int][Math]::Floor($usable * 5.0 / 9.0)
        $lowerMinimum = $usable - $upperMinimum
    }

    $lowerBound = $upperMinimum
    $upperBound = $usable - $lowerMinimum
    if ($CurrentDistance -lt 0) {
        $distance = [int][Math]::Round($usable * 0.45, [System.MidpointRounding]::AwayFromZero)
    } else {
        $distance = $CurrentDistance
    }
    $distance = [Math]::Max($lowerBound, [Math]::Min($upperBound, $distance))
    return [pscustomobject]@{
        UpperMinimum = $upperMinimum
        LowerMinimum = $lowerMinimum
        Distance = $distance
    }
}

function Set-UiStatus {
    param([Parameter(Mandatory = $true)][string]$Message, [System.Drawing.Color]$Color = [System.Drawing.Color]::MidnightBlue)
    if ($null -ne $script:statusLabel -and !$script:statusLabel.IsDisposed) {
        $script:statusLabel.Text = $Message
        $script:statusLabel.ForeColor = $Color
    }
}

function Get-BoxText {
    param([Parameter(Mandatory = $true)][System.Windows.Forms.TextBox]$Box)
    return $Box.Text.Trim()
}

function New-RandomToken {
    param([int]$ByteCount = 32)
    if ($ByteCount -lt 16) { throw "随机令牌长度不能少于 16 字节。" }
    $bytes = New-Object byte[] $ByteCount
    $random = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($bytes) } finally { $random.Dispose() }
    return ([Convert]::ToBase64String($bytes)).TrimEnd("=").Replace("+", "-").Replace("/", "_")
}

function Test-GlobalIPv6 {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { return $false }
    try {
        $parsed = [System.Net.IPAddress]::Parse($Value.Trim('[', ']'))
        $bytes = $parsed.GetAddressBytes()
        return $bytes.Length -eq 16 -and (($bytes[0] -band 0xe0) -eq 0x20)
    } catch {
        return $false
    }
}

function Get-DetectedIPv6Address {
    try {
        $addresses = @(Get-NetIPAddress -AddressFamily IPv6 -ErrorAction Stop)
    } catch {
        return ""
    }
    $candidates = foreach ($entry in $addresses) {
        $value = ([string]$entry.IPAddress).Trim()
        if ([string]::IsNullOrWhiteSpace($value)) { continue }
        if ([string]$entry.AddressState -ne "Preferred") { continue }
        if ([bool]$entry.SkipAsSource) { continue }
        if (!(Test-GlobalIPv6 $value)) { continue }
        try {
            $parsed = [System.Net.IPAddress]::Parse($value)
            if ($parsed.GetAddressBytes().Length -ne 16) { continue }
            [pscustomobject]@{
                Address = $value
                PrefixOrigin = [string]$entry.PrefixOrigin
                InterfaceIndex = [int]$entry.InterfaceIndex
            }
        } catch {
            continue
        }
    }
    $selected = @($candidates | Sort-Object @{ Expression = { if ($_.PrefixOrigin -eq "RouterAdvertisement") { 0 } else { 1 } } }, @{ Expression = { $_.InterfaceIndex } })
    if ($selected.Count -gt 0) { return [string]$selected[0].Address }
    return ""
}

function Update-ControlEndpoint {
    $address = (Get-BoxText $script:ipv6AddressBox).Trim('[', ']')
    if ($address -eq "" -or !(Test-GlobalIPv6 $address)) {
        throw "房主必须使用可从公网访问的 2000::/3 全局 IPv6 地址。"
    }
    $script:updatingEndpoint = $true
    try {
        $script:ipv6AddressBox.Text = $address
        $script:controlUrl = "http://[$address]:8080"
        if ($null -ne $script:controlUrlBox) {
            $script:controlUrlBox.Text = $script:controlUrl
        }
    } finally {
        $script:updatingEndpoint = $false
    }
    return $script:controlUrl
}

function Refresh-LocalIPv6 {
    try {
        $address = Get-DetectedIPv6Address
        if ($address -eq "") {
            throw "没有检测到首选且非 SkipAsSource 的 2000::/3 全局 IPv6 地址。"
        }
        $script:ipv6AddressBox.Text = $address
        $null = Update-ControlEndpoint
        $script:hostStartButton.Enabled = !$script:primaryBusy
        Add-UiLog "已检测本机全局 IPv6；房主控制面地址已准备。"
        Set-UiStatus "房主 IPv6 已就绪" ([System.Drawing.Color]::ForestGreen)
        return $true
    } catch {
        $script:hostStartButton.Enabled = $false
        Add-UiLog "检测房主 IPv6 失败：$($_.Exception.Message)" "警告"
        Set-UiStatus "需要可访问的全局 IPv6" ([System.Drawing.Color]::Firebrick)
        return $false
    }
}

function Get-PowerShellPath {
    $candidate = Join-Path $env:WINDIR "System32\WindowsPowerShell\v1.0\powershell.exe"
    if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    return "powershell.exe"
}

function Get-PayloadExecutable {
    param([Parameter(Mandatory = $true)][string]$Name)
    $packaged = Join-Path $PackageDirectory $Name
    if (Test-Path -LiteralPath $packaged -PathType Leaf) { return $packaged }
    $installed = Join-Path $InstallDirectory $Name
    if (Test-Path -LiteralPath $installed -PathType Leaf) { return $installed }
    throw "找不到 $Name；请先安装或重新下载完整安装器。"
}

function Quote-ProcessArgument {
    param([AllowEmptyString()][string]$Value)
    if ($null -eq $Value) { return '""' }
    if ($Value -notmatch '[\s"]') { return $Value }
    $escaped = $Value -replace '(\\*)"', '$1$1\"'
    $escaped = $escaped -replace '(\\+)$', '$1$1'
    return '"' + $escaped + '"'
}

function Get-ClientEnvironment {
    $environment = @{}
    if (![string]::IsNullOrWhiteSpace($script:controlUrl)) {
        $environment["IPV6MESH_CONTROL_URL"] = $script:controlUrl
    }
    if (![string]::IsNullOrWhiteSpace($script:adminToken)) {
        $environment["IPV6MESH_ADMIN_TOKEN"] = $script:adminToken
    }
    return $environment
}

function Invoke-External {
    param(
        [Parameter(Mandatory = $true)][string]$FileName,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [hashtable]$Environment = @{},
        [Parameter(Mandatory = $true)][string]$Source,
        [switch]$SuppressStandardOutput,
        [switch]$Quiet
    )
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $FileName
    $psi.Arguments = (($Arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join " ")
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($key in $Environment.Keys) {
        $psi.EnvironmentVariables[$key] = [string]$Environment[$key]
    }
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $psi
    if (!$Quiet) { Add-UiLog "开始执行 $Source" "调试" }
    try {
        [void]$process.Start()
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.Result
        $stderr = $stderrTask.Result
        $exitCode = $process.ExitCode
    } catch {
        if (!$Quiet) { Add-UiLog "$Source 启动失败：$($_.Exception.Message)" "错误" }
        throw
    } finally {
        $process.Dispose()
    }
    if (!$Quiet -and !$SuppressStandardOutput -and ![string]::IsNullOrWhiteSpace($stdout)) {
        foreach ($line in ($stdout -split [Environment]::NewLine)) {
            if (![string]::IsNullOrWhiteSpace($line)) { Add-UiLog "$Source：$line" }
        }
    }
    if (!$Quiet -and ![string]::IsNullOrWhiteSpace($stderr)) {
        foreach ($line in ($stderr -split [Environment]::NewLine)) {
            if (![string]::IsNullOrWhiteSpace($line)) { Add-UiLog "$Source：$line" "警告" }
        }
    }
    if (!$Quiet -and $exitCode -ne 0) { Add-UiLog "$Source 退出码：$exitCode" "错误" }
    return [pscustomobject]@{ ExitCode = $exitCode; Stdout = $stdout; Stderr = $stderr }
}

function Invoke-VpnCtl {
    param([Parameter(Mandatory = $true)][string[]]$Arguments, [switch]$SuppressStandardOutput, [switch]$Quiet)
    $spec = @{
        FileName = (Get-PayloadExecutable "vpnctl.exe")
        Arguments = $Arguments
        Environment = (Get-ClientEnvironment)
        Source = "vpnctl"
        SuppressStandardOutput = $SuppressStandardOutput
        Quiet = $Quiet
    }
    return Invoke-External @spec
}

function Assert-ControlUrl {
    $value = [string]$script:controlUrl
    $parsed = $null
    if (![Uri]::TryCreate($value, [UriKind]::Absolute, [ref]$parsed) -or $parsed.Scheme -notin @("http", "https") -or [string]::IsNullOrWhiteSpace($parsed.Host)) {
        throw "控制面地址无效。"
    }
    return $value.TrimEnd("/")
}

function Convert-ResultToJson {
    param([Parameter(Mandatory = $true)]$Result, [Parameter(Mandatory = $true)][string]$Operation, [switch]$Quiet)
    if ($Result.ExitCode -ne 0) {
        $stderr = [string]$Result.Stderr
        $code = ""
        if ($stderr -match '(room_not_ready|room_mode_disabled|node_already_joined|room_full|join_rate_limited|enrollment_recovery_pending|invalid_node|request_too_large|unauthorized)') {
            $code = [string]$Matches[1]
        }
        $messages = @{
            room_not_ready = "房主尚未完成创建网络。"
            room_mode_disabled = "目标控制面未启用房间模式。"
            node_already_joined = "本机已经加入当前房间。"
            room_full = "房间地址池已满。"
            join_rate_limited = "加入过于频繁，请稍后重试。"
            enrollment_recovery_pending = "加入结果待恢复，请稍后刷新状态。"
            invalid_node = "节点信息无效，请检查本机服务。"
            request_too_large = "请求内容超出允许大小。"
            unauthorized = "控制面拒绝了当前操作。"
        }
        if ($code -ne "" -and $messages.ContainsKey($code)) {
            if (!$Quiet) { Add-UiLog "$Operation 失败，错误码：$code" "警告" }
            throw "$Operation 失败：$($messages[$code])"
        }
        if (!$Quiet) { Add-UiLog "$Operation 失败；原始错误已隐藏。" "错误" }
        throw "$Operation 失败，请查看诊断日志。"
    }
    if ([string]::IsNullOrWhiteSpace($Result.Stdout)) {
        throw "$Operation 没有返回 JSON 结果。"
    }
    try {
        return ($Result.Stdout | ConvertFrom-Json -ErrorAction Stop)
    } catch {
        if (!$Quiet) { Add-UiLog "$Operation 返回内容不是有效 JSON。" "错误" }
        throw "$Operation 返回内容无效。"
    }
}

function Get-WebException {
    param([AllowNull()][object]$Exception)
    while ($null -ne $Exception) {
        if ($Exception -is [System.Net.WebException]) { return $Exception }
        $Exception = $Exception.InnerException
    }
    return $null
}

function Test-ControlHealth {
    param([switch]$Quiet)
    try {
        $url = Assert-ControlUrl
        $request = [System.Net.HttpWebRequest]::Create($url + "/healthz")
        $request.Proxy = $null
        $request.Timeout = 5000
        $response = $request.GetResponse()
        try { $response.Dispose() } finally {}
        if (!$Quiet) { Set-UiStatus "控制面可访问" ([System.Drawing.Color]::ForestGreen) }
        return $true
    } catch {
        if (!$Quiet) {
            $webException = Get-WebException $_.Exception
            if ($null -ne $webException) {
                Add-UiLog "控制面暂不可访问，请确认房主流程仍在运行。" "警告"
            } else {
                Add-UiLog "控制面健康检查失败。" "错误"
            }
            Set-UiStatus "控制面不可访问" ([System.Drawing.Color]::Firebrick)
        }
        return $false
    }
}

function Wait-ControlPlaneReady {
    param([int]$TimeoutSeconds = 15)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        if ($script:controlProcess -and $script:controlProcess.HasExited) {
            Add-UiLog "控制面进程已退出，未能完成健康检查。" "错误"
            return $false
        }
        if (Test-ControlHealth -Quiet) {
            Add-UiLog "控制面已就绪。"
            return $true
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    Add-UiLog ('控制面进程已启动，但 {0} 秒内仍未响应 /healthz；可稍后点击“检查健康”。' -f $TimeoutSeconds) -Level '警告'
    Set-UiStatus "控制面等待中" ([System.Drawing.Color]::DarkOrange)
    return $false
}

function Open-ControlFirewall {
    param([Parameter(Mandatory = $true)][int]$Port)
    $ruleName = "IPv6Mesh Control Plane TCP $Port"
    try {
        if (!(Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) {
            New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Protocol TCP -LocalPort $Port -Action Allow -Profile Any | Out-Null
            Add-UiLog "已放行控制面 TCP $Port 防火墙规则。"
        }
    } catch {
        Add-UiLog "自动放行控制面防火墙失败，请手动放行 TCP $Port。" "警告"
    }
}

function Start-ControlPlane {
    $url = Assert-ControlUrl
    $parsed = [Uri]$url
    $port = if ($parsed.Port -gt 0) { $parsed.Port } else { 8080 }
    $listenAddress = "[::]:$port"
    if ($script:controlProcess -and !$script:controlProcess.HasExited) {
        Add-UiLog "控制面已经由本窗口启动。"
        return
    }
    $script:adminToken = New-RandomToken
    Open-ControlFirewall $port
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = Get-PayloadExecutable "control-server.exe"
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.EnvironmentVariables["CONTROL_LISTEN_ADDRESS"] = $listenAddress
    $psi.EnvironmentVariables["CONTROL_BOOTSTRAP_TOKEN"] = $script:adminToken
    $psi.EnvironmentVariables["CONTROL_ROOM_MODE"] = "true"
    $psi.EnvironmentVariables["CONTROL_REPOSITORY_MODE"] = "memory"
    $psi.EnvironmentVariables["CONTROL_SESSION_TTL"] = "24h"
    $psi.EnvironmentVariables["CONTROL_INVITE_TTL"] = "24h"
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $psi
    $process.EnableRaisingEvents = $true
    $sourceBase = "IPv6Mesh.Control.$PID"
    $script:controlEventSources = @("$sourceBase.Stdout", "$sourceBase.Stderr", "$sourceBase.Exited")
    [void](Register-ObjectEvent -InputObject $process -EventName OutputDataReceived -SourceIdentifier $script:controlEventSources[0] -Action {
        if ($EventArgs.Data) { Add-UiLog $EventArgs.Data "控制面" }
    })
    [void](Register-ObjectEvent -InputObject $process -EventName ErrorDataReceived -SourceIdentifier $script:controlEventSources[1] -Action {
        if ($EventArgs.Data) { Add-UiLog $EventArgs.Data "控制面" }
    })
    [void](Register-ObjectEvent -InputObject $process -EventName Exited -SourceIdentifier $script:controlEventSources[2] -Action {
        Add-UiLog "控制面进程已退出。" "警告"
    })
    [void]$process.Start()
    $process.BeginOutputReadLine()
    $process.BeginErrorReadLine()
    $script:controlProcess = $process
    $script:startedControlPlane = $true
    Add-UiLog "控制面已启动，使用临时内存房间模式。"
    Set-UiStatus "控制面启动中" ([System.Drawing.Color]::DarkOrange)
    if (!(Wait-ControlPlaneReady)) {
        throw "控制面未能就绪。"
    }
}

function Stop-ControlPlane {
    foreach ($source in $script:controlEventSources) {
        try { Get-EventSubscriber -SourceIdentifier $source -ErrorAction SilentlyContinue | Unregister-Event -Force -ErrorAction SilentlyContinue } catch {}
    }
    $script:controlEventSources = @()
    if ($script:controlProcess) {
        try {
            if (!$script:controlProcess.HasExited) {
                $script:controlProcess.Kill()
                $script:controlProcess.WaitForExit()
                Add-UiLog "已停止本窗口启动的控制面进程。"
            }
        } catch {
            Add-UiLog "停止控制面进程失败。" "警告"
        } finally {
            $script:controlProcess.Dispose()
            $script:controlProcess = $null
        }
    }
    $script:adminToken = ""
    $script:startedControlPlane = $false
}

function Stop-NodeService {
    try {
        $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($null -eq $service) { return }
        if ($service.Status -ne [System.ServiceProcess.ServiceControllerStatus]::Stopped) {
            Stop-Service -Name $ServiceName -Force -ErrorAction Stop
        }
        Add-UiLog "IPv6Mesh 节点服务已停止，本机网络资源已释放。"
    } catch {
        Add-UiLog "停止 IPv6Mesh 节点服务失败。" "警告"
    }
}

function Stop-StartedResources {
    if ($script:startedNodeService) {
        Stop-NodeService
        $script:startedNodeService = $false
    }
    if ($script:startedControlPlane) {
        Stop-ControlPlane
    }
}

function Stop-AllResources {
    if ($script:cleanupStarted) { return }
    $script:cleanupStarted = $true
    Add-UiLog "正在执行退出清理。"
    Dispose-StatusRefreshTimer
    Stop-StartedResources
    Add-UiLog "退出清理完成。"
}

function Install-NodeService {
    param([Parameter(Mandatory = $true)][string]$ControlUrl)
    try {
        $resolvedUrl = $ControlUrl.TrimEnd("/")
        if ([string]::IsNullOrWhiteSpace($resolvedUrl)) { throw "控制面地址为空。" }
        $installScript = Join-Path $PackageDirectory "install.ps1"
        if (!(Test-Path -LiteralPath $installScript -PathType Leaf)) { throw "载荷中缺少 install.ps1。" }
        $arguments = @("-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", $installScript, "-PackageDirectory", $PackageDirectory, "-ControlUrl", $resolvedUrl, "-InstallDirectory", $InstallDirectory, "-DataDirectory", $DataDirectory, "-ServiceName", $ServiceName, "-StartService")
        $result = Invoke-External -FileName (Get-PowerShellPath) -Arguments $arguments -Source "安装节点服务"
        if ($result.ExitCode -ne 0) { throw "安装脚本失败。" }
        $script:startedNodeService = $true
        Set-UiStatus "节点服务已启动" ([System.Drawing.Color]::ForestGreen)
        return $true
    } catch {
        Add-UiLog "安装节点服务失败。" "错误"
        return $false
    }
}

function Get-NodeStatus {
	param([switch]$Automatic)
	if ($script:statusRefreshInProgress) { return $null }
	$script:statusRefreshInProgress = $true
    try {
        $result = Invoke-VpnCtl -Arguments @("status") -SuppressStandardOutput -Quiet:$Automatic
        $status = Convert-ResultToJson $result "读取节点状态" -Quiet:$Automatic
        $script:activeNetworkId = [string]$status.network_id
        $virtualIPv4 = [string]$status.virtual_ipv4
        $path = [string]$status.path_state
        $errorCode = [string]$status.last_error
        $summary = "本机虚拟 IPv4：$virtualIPv4    路径：$path"
        if ($errorCode -ne "") { $summary += "    错误码：$errorCode" }
        if ($null -ne $script:nodeStatusLabel -and !$script:nodeStatusLabel.IsDisposed) {
            $script:nodeStatusLabel.Text = $summary
        }
        $fingerprint = "$virtualIPv4|$path|$errorCode"
        $decision = Get-StatusLogDecision -Automatic ([bool]$Automatic) -Succeeded $true -Fingerprint $fingerprint -HasPrevious $script:hasStatusRefreshResult -PreviousSucceeded $script:lastStatusRefreshSucceeded -PreviousFingerprint $script:lastStatusFingerprint
        if ($decision -eq "Recovered") {
            Add-UiLog "节点状态读取已恢复：VirtualIPv4=$virtualIPv4，Path=$path，ErrorCode=$errorCode"
        } elseif ($decision -in @("Changed", "Manual")) {
            Add-UiLog "节点状态已刷新：VirtualIPv4=$virtualIPv4，Path=$path，ErrorCode=$errorCode"
        }
        $script:hasStatusRefreshResult = $true
        $script:lastStatusRefreshSucceeded = $true
        $script:lastStatusFingerprint = $fingerprint
        return $status
    } catch {
        $decision = Get-StatusLogDecision -Automatic ([bool]$Automatic) -Succeeded $false -Fingerprint "" -HasPrevious $script:hasStatusRefreshResult -PreviousSucceeded $script:lastStatusRefreshSucceeded -PreviousFingerprint $script:lastStatusFingerprint
        if ($decision -in @("Failed", "Manual")) { Add-UiLog "读取节点状态失败。" "错误" }
        if ($null -ne $script:nodeStatusLabel -and !$script:nodeStatusLabel.IsDisposed) {
            $script:nodeStatusLabel.Text = "节点服务未连接或尚未加入房间"
        }
        $script:hasStatusRefreshResult = $true
        $script:lastStatusRefreshSucceeded = $false
        $script:lastStatusFingerprint = ""
        return $null
    } finally {
        $script:statusRefreshInProgress = $false
    }
}

function Set-ActiveVirtualIPv4 {
    param([Parameter(Mandatory = $true)]$Joined, [Parameter(Mandatory = $true)][string]$Role)
    $virtualIPv4 = [string]$Joined.virtual_ipv4
    if ($Role -eq "Host") {
        $script:hostVirtualIPv4Label.Text = "房主虚拟 IPv4：$virtualIPv4"
    } else {
        $script:memberVirtualIPv4Label.Text = "本机虚拟 IPv4：$virtualIPv4"
    }
}

function Start-HostRoom {
    Set-PrimaryBusy $true "正在创建房间并连接本机……"
    try {
        if (!(Refresh-LocalIPv6)) { throw "没有可用的房主全局 IPv6。" }
        $hostIPv6 = Get-BoxText $script:ipv6AddressBox
        $endpoint = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "endpoint", "--host-ipv6", $hostIPv6) -SuppressStandardOutput) "验证房主 IPv6"
        $script:controlUrl = [string]$endpoint.control_url
        $script:controlUrlBox.Text = $script:controlUrl
        Start-ControlPlane
        $roomName = "IPv6Mesh-$env:COMPUTERNAME"
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "create", "--name", $roomName, "--pool", "10.42.0.0/24") -SuppressStandardOutput) "创建房间"
        if (!(Install-NodeService -ControlUrl $script:controlUrl)) { throw "节点服务安装失败。" }
        $joined = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "join", "--host-ipv6", $hostIPv6, "--name", $env:COMPUTERNAME) -SuppressStandardOutput) "房主加入房间"
        $script:activeNetworkId = [string]$joined.network_id
        Set-ActiveVirtualIPv4 $joined "Host"
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", $script:activeNetworkId) -SuppressStandardOutput) "连接虚拟网络"
        $null = Get-NodeStatus
        Set-UiStatus "房主已连接" ([System.Drawing.Color]::ForestGreen)
        Add-UiLog "房间创建完成；可将房主 IPv6 提供给成员。"
    } catch {
        Add-UiLog "创建房间失败，正在清理本次启动的资源。" "错误"
        Stop-StartedResources
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("创建房间失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    } finally {
        Set-PrimaryBusy $false ""
    }
}

function Join-MemberRoom {
    Set-PrimaryBusy $true "正在加入房间并连接本机……"
    try {
        $hostIPv6 = Get-BoxText $script:memberHostIPv6Box
        if ($hostIPv6 -eq "") { throw "请输入房主 IPv6。" }
        $endpoint = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "endpoint", "--host-ipv6", $hostIPv6) -SuppressStandardOutput) "验证房主 IPv6"
        $script:controlUrl = [string]$endpoint.control_url
        $script:controlUrlBox.Text = $script:controlUrl
        if (!(Install-NodeService -ControlUrl $script:controlUrl)) { throw "节点服务安装失败。" }
        $joined = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "join", "--host-ipv6", $hostIPv6, "--name", $env:COMPUTERNAME) -SuppressStandardOutput) "加入房间"
        $script:activeNetworkId = [string]$joined.network_id
        Set-ActiveVirtualIPv4 $joined "Member"
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", $script:activeNetworkId) -SuppressStandardOutput) "连接虚拟网络"
        $null = Get-NodeStatus
        Set-UiStatus "成员已连接" ([System.Drawing.Color]::ForestGreen)
        Add-UiLog "已加入房间并连接本机。"
    } catch {
        Add-UiLog "加入房间失败，正在清理本次启动的资源。" "错误"
        Stop-StartedResources
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("加入房间失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    } finally {
        Set-PrimaryBusy $false ""
    }
}

function Connect-Node {
    if ([string]::IsNullOrWhiteSpace($script:activeNetworkId)) {
        $null = Get-NodeStatus
    }
    if ([string]::IsNullOrWhiteSpace($script:activeNetworkId)) { throw "本机尚未加入房间。" }
    $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", $script:activeNetworkId) -SuppressStandardOutput) "连接虚拟网络"
    $null = Get-NodeStatus
    Set-UiStatus "节点已连接" ([System.Drawing.Color]::ForestGreen)
}

function Disconnect-Node {
    if ([string]::IsNullOrWhiteSpace($script:activeNetworkId)) { return }
    $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("disconnect", "--network", $script:activeNetworkId) -SuppressStandardOutput) "断开虚拟网络"
    $null = Get-NodeStatus
    Set-UiStatus "节点已断开" ([System.Drawing.Color]::DarkOrange)
}

function Leave-Node {
    if ([string]::IsNullOrWhiteSpace($script:activeNetworkId)) { return }
    $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("leave", "--network", $script:activeNetworkId) -SuppressStandardOutput) "离开房间"
    $script:activeNetworkId = ""
    if ($null -ne $script:nodeStatusLabel) { $script:nodeStatusLabel.Text = "本机尚未加入房间" }
    Set-UiStatus "节点未加入房间" ([System.Drawing.Color]::DarkOrange)
}

function Copy-UiField {
    param([Parameter(Mandatory = $true)][System.Windows.Forms.TextBox]$Box, [Parameter(Mandatory = $true)][string]$Description)
    $value = Get-BoxText $Box
    if ($value -eq "") { return }
    try {
        [System.Windows.Forms.Clipboard]::SetText($value)
        Add-UiLog "已复制 $Description；日志不会记录其正文。"
    } catch {
        Add-UiLog "复制 $Description 失败。" "错误"
    }
}

function Export-UiLog {
    $dialog = New-Object System.Windows.Forms.SaveFileDialog
    $dialog.Filter = "日志文件 (*.log)|*.log|文本文件 (*.txt)|*.txt|所有文件 (*.*)|*.*"
    $dialog.FileName = "ipv6mesh-debug-{0}.log" -f (Get-Date -Format "yyyyMMdd-HHmmss")
    if ($dialog.ShowDialog($script:form) -ne [System.Windows.Forms.DialogResult]::OK) { return }
    try {
        [IO.File]::WriteAllText($dialog.FileName, ($script:logLines -join [Environment]::NewLine) + [Environment]::NewLine, (New-Object System.Text.UTF8Encoding($false)))
        Add-UiLog "日志已导出。"
    } catch {
        Add-UiLog "导出日志失败。" "错误"
    }
}

function Set-PrimaryBusy {
    param([bool]$Busy, [string]$Status = "")
    $script:primaryBusy = $Busy
    if ($null -ne $script:hostStartButton) {
        $hostReady = $false
        if ($null -ne $script:ipv6AddressBox) { $hostReady = Test-GlobalIPv6 (Get-BoxText $script:ipv6AddressBox) }
        $script:hostStartButton.Enabled = !$Busy -and $hostReady
    }
    if ($null -ne $script:memberJoinButton) { $script:memberJoinButton.Enabled = !$Busy }
    foreach ($button in $script:backButtons) {
        if ($null -ne $button) { $button.Enabled = !$Busy }
    }
    if (![string]::IsNullOrWhiteSpace($Status)) {
        Set-UiStatus $Status ([System.Drawing.Color]::DarkOrange)
    }
}

function Invoke-AutomaticStatusRefresh {
    if ($script:primaryBusy -or $script:cleanupStarted) { return }
    if ($script:activePage -eq "Welcome" -or $script:statusRefreshInProgress) { return }
    $null = Get-NodeStatus -Automatic
}

function Stop-StatusRefresh {
    if ($null -ne $script:statusRefreshTimer) {
        $script:statusRefreshTimer.Stop()
    }
}

function Start-StatusRefresh {
    if ($null -eq $script:statusRefreshTimer) { return }
    Invoke-AutomaticStatusRefresh
    if ($script:activePage -ne "Welcome" -and !$script:cleanupStarted) {
        $script:statusRefreshTimer.Start()
    }
}

function Dispose-StatusRefreshTimer {
    Stop-StatusRefresh
    if ($null -ne $script:statusRefreshTimer) {
        $script:statusRefreshTimer.Dispose()
        $script:statusRefreshTimer = $null
    }
}

function Show-Page {
    param([ValidateSet("Welcome", "Host", "Member")][string]$Name)
    $script:activePage = $Name
    if ($null -ne $script:welcomePanel) { $script:welcomePanel.Visible = ($Name -eq "Welcome") }
    if ($null -ne $script:hostPanel) { $script:hostPanel.Visible = ($Name -eq "Host") }
    if ($null -ne $script:memberPanel) { $script:memberPanel.Visible = ($Name -eq "Member") }
    if ($null -ne $script:diagnosticsPanel) {
        $script:diagnosticsPanel.Visible = ($Name -ne "Welcome")
        if ($Name -ne "Welcome") { $script:diagnosticsPanel.BringToFront() }
    }
    if ($Name -eq "Welcome") {
        Stop-StatusRefresh
    } else {
        Start-StatusRefresh
    }
}

function Show-WelcomePage {
    Show-Page "Welcome"
    Set-UiStatus "等待选择" ([System.Drawing.Color]::MidnightBlue)
}

function Show-HostPage {
    Show-Page "Host"
    $null = Refresh-LocalIPv6
}

function Show-MemberPage {
    Show-Page "Member"
    Set-UiStatus "请输入房主 IPv6" ([System.Drawing.Color]::MidnightBlue)
}

function New-Label {
    param([string]$Text, [int]$X, [int]$Y, [int]$Width = 100, [int]$Height = 24, [int]$FontSize = 9)
    $label = New-Object System.Windows.Forms.Label
    $label.Text = $Text
    $label.Location = New-Object System.Drawing.Point($X, $Y)
    $label.Size = New-Object System.Drawing.Size($Width, $Height)
    $label.TextAlign = [System.Drawing.ContentAlignment]::MiddleLeft
    $label.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", $FontSize)
    return $label
}

function New-TextBox {
    param([int]$X, [int]$Y, [int]$Width = 200, [int]$Height = 24, [switch]$Password, [switch]$ReadOnly)
    $box = New-Object System.Windows.Forms.TextBox
    $box.Location = New-Object System.Drawing.Point($X, $Y)
    $box.Size = New-Object System.Drawing.Size($Width, $Height)
    $box.UseSystemPasswordChar = $Password
    $box.ReadOnly = $ReadOnly
    return $box
}

function New-Button {
    param([string]$Text, [int]$X, [int]$Y, [int]$Width = 120, [int]$Height = 30)
    $button = New-Object System.Windows.Forms.Button
    $button.Text = $Text
    $button.Location = New-Object System.Drawing.Point($X, $Y)
    $button.Size = New-Object System.Drawing.Size($Width, $Height)
    return $button
}

$initialIPv6 = Get-DetectedIPv6Address
if ([string]::IsNullOrWhiteSpace($initialIPv6) -and ![string]::IsNullOrWhiteSpace($ControlUrl)) {
    try {
        $providedUri = [Uri]$ControlUrl
        if ($providedUri.Host.Contains(":")) { $initialIPv6 = $providedUri.Host }
    } catch {}
}

$script:form = New-Object System.Windows.Forms.Form
$script:form.Text = "IPv6Mesh 远程组网"
$script:form.StartPosition = [System.Windows.Forms.FormStartPosition]::CenterScreen
$script:form.ClientSize = New-Object System.Drawing.Size(1120, 720)
$script:form.MinimumSize = New-Object System.Drawing.Size(1120, 720)
$script:form.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 9)

$title = New-Label "IPv6Mesh 远程组网" 20 15 500 32
$title.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 15, [System.Drawing.FontStyle]::Bold)
$script:form.Controls.Add($title)
$script:statusLabel = New-Label "等待选择" 620 18 470 28
$script:statusLabel.TextAlign = [System.Drawing.ContentAlignment]::MiddleRight
$script:form.Controls.Add($script:statusLabel)

$script:welcomePanel = New-Object System.Windows.Forms.Panel
$script:welcomePanel.Location = New-Object System.Drawing.Point(20, 70)
$script:welcomePanel.Size = New-Object System.Drawing.Size(1080, 570)
$script:form.Controls.Add($script:welcomePanel)
$script:welcomePanel.Controls.Add((New-Label "你想做什么？" 20 20 500 40 22))
$script:welcomePanel.Controls.Add((New-Label "选择一种方式开始 IPv6Mesh 房间流程。" 22 70 600 28))
$createButton = New-Button "创建网络" 180 150 260 70
$createButton.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 12)
$createButton.Add_Click({ Show-HostPage })
$script:welcomePanel.Controls.Add($createButton)
$joinButton = New-Button "加入网络" 540 150 260 70
$joinButton.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 12)
$joinButton.Add_Click({ Show-MemberPage })
$script:welcomePanel.Controls.Add($joinButton)

$script:hostPanel = New-Object System.Windows.Forms.Panel
$script:hostPanel.Location = New-Object System.Drawing.Point(20, 70)
$script:hostPanel.Size = New-Object System.Drawing.Size(1080, 570)
$script:hostPanel.Visible = $false
$script:form.Controls.Add($script:hostPanel)
$hostBackButton = New-Button "返回" 20 15 90
$hostBackButton.Add_Click({ Show-WelcomePage })
$script:backButtons += $hostBackButton
$script:hostPanel.Controls.Add($hostBackButton)
$script:hostPanel.Controls.Add((New-Label "创建网络" 135 15 300 32 18))
$script:hostPanel.Controls.Add((New-Label "房主 IPv6：" 40 85 130 28))
$script:ipv6AddressBox = New-TextBox 170 82 540
$script:ipv6AddressBox.Text = $initialIPv6
$script:hostPanel.Controls.Add($script:ipv6AddressBox)
$detectButton = New-Button "重新检测" 730 80 120
$detectButton.Add_Click({ $null = Refresh-LocalIPv6 })
$script:hostPanel.Controls.Add($detectButton)
$script:hostPanel.Controls.Add((New-Label "房主 IPv6 仅接受首选、非 SkipAsSource 的 2000::/3 全局地址。" 170 115 700 25))
$script:hostPanel.Controls.Add((New-Label "控制面地址：" 40 160 130 28))
$script:controlUrlBox = New-TextBox 170 157 540 -ReadOnly
$script:hostPanel.Controls.Add($script:controlUrlBox)
$copyHostIPv6Button = New-Button "复制房主 IPv6" 730 155 140
$copyHostIPv6Button.Add_Click({ Copy-UiField $script:ipv6AddressBox "房主 IPv6" })
$script:hostPanel.Controls.Add($copyHostIPv6Button)
$script:hostVirtualIPv4Label = New-Label "房主虚拟 IPv4：未加入" 40 220 650 30 12
$script:hostPanel.Controls.Add($script:hostVirtualIPv4Label)
$script:hostStartButton = New-Button "创建并连接" 40 275 190 44
$script:hostStartButton.Add_Click({ Start-HostRoom })
$script:hostPanel.Controls.Add($script:hostStartButton)

$script:memberPanel = New-Object System.Windows.Forms.Panel
$script:memberPanel.Location = New-Object System.Drawing.Point(20, 70)
$script:memberPanel.Size = New-Object System.Drawing.Size(1080, 570)
$script:memberPanel.Visible = $false
$script:form.Controls.Add($script:memberPanel)
$memberBackButton = New-Button "返回" 20 15 90
$memberBackButton.Add_Click({ Show-WelcomePage })
$script:backButtons += $memberBackButton
$script:memberPanel.Controls.Add($memberBackButton)
$script:memberPanel.Controls.Add((New-Label "加入网络" 135 15 300 32 18))
$script:memberPanel.Controls.Add((New-Label "房主 IPv6：" 40 85 130 28))
$script:memberHostIPv6Box = New-TextBox 170 82 680
$script:memberPanel.Controls.Add($script:memberHostIPv6Box)
$script:memberPanel.Controls.Add((New-Label "成员只需输入房主 IPv6；地址必须是 2000::/3 全局 IPv6。" 170 115 700 25))
$script:memberPanel.Controls.Add((New-Label "本机名称：" 40 160 130 28))
$script:memberNameLabel = New-Label ([string]$env:COMPUTERNAME) 170 160 500 28
$script:memberPanel.Controls.Add($script:memberNameLabel)
$script:memberVirtualIPv4Label = New-Label "本机虚拟 IPv4：未加入" 40 220 650 30 12
$script:memberPanel.Controls.Add($script:memberVirtualIPv4Label)
$script:memberJoinButton = New-Button "加入并连接" 40 275 190 44
$script:memberJoinButton.Add_Click({ Join-MemberRoom })
$script:memberPanel.Controls.Add($script:memberJoinButton)

$script:diagnosticsPanel = New-Object System.Windows.Forms.GroupBox
$script:diagnosticsPanel.Text = "诊断与日志"
$script:diagnosticsPanel.Location = New-Object System.Drawing.Point(40, 350)
$script:diagnosticsPanel.Size = New-Object System.Drawing.Size(1040, 290)
$script:diagnosticsPanel.Visible = $false
$script:form.Controls.Add($script:diagnosticsPanel)
$script:nodeStatusLabel = New-Label "节点服务未检查状态" 20 25 980 28
$script:diagnosticsPanel.Controls.Add($script:nodeStatusLabel)
$refreshStatusButton = New-Button "刷新状态" 20 60 100
$refreshStatusButton.Add_Click({ $null = Get-NodeStatus })
$script:diagnosticsPanel.Controls.Add($refreshStatusButton)
$connectButton = New-Button "连接" 130 60 90
$connectButton.Add_Click({ Connect-Node })
$script:diagnosticsPanel.Controls.Add($connectButton)
$disconnectButton = New-Button "断开" 230 60 90
$disconnectButton.Add_Click({ Disconnect-Node })
$script:diagnosticsPanel.Controls.Add($disconnectButton)
$leaveButton = New-Button "离开房间" 330 60 110
$leaveButton.Add_Click({ Leave-Node })
$script:diagnosticsPanel.Controls.Add($leaveButton)
$script:logBox = New-Object System.Windows.Forms.TextBox
$script:logBox.Multiline = $true
$script:logBox.ReadOnly = $true
$script:logBox.ScrollBars = [System.Windows.Forms.ScrollBars]::Both
$script:logBox.WordWrap = $false
$script:logBox.Location = New-Object System.Drawing.Point(20, 100)
$script:logBox.Size = New-Object System.Drawing.Size(1000, 145)
$script:logBox.Font = New-Object System.Drawing.Font("Consolas", 9)
$script:diagnosticsPanel.Controls.Add($script:logBox)
$clearLogButton = New-Button "清空日志" 20 250 100
$clearLogButton.Add_Click({ $script:logLines.Clear(); $script:logBox.Clear() })
$script:diagnosticsPanel.Controls.Add($clearLogButton)
$copyLogButton = New-Button "复制日志" 130 250 100
$copyLogButton.Add_Click({ [System.Windows.Forms.Clipboard]::SetText(($script:logLines -join [Environment]::NewLine)) })
$script:diagnosticsPanel.Controls.Add($copyLogButton)
$exportLogButton = New-Button "导出日志" 240 250 100
$exportLogButton.Add_Click({ Export-UiLog })
$script:diagnosticsPanel.Controls.Add($exportLogButton)

$script:statusRefreshTimer = New-Object System.Windows.Forms.Timer
$script:statusRefreshTimer.Interval = 2000
$script:statusRefreshTimer.Add_Tick({ Invoke-AutomaticStatusRefresh })

$script:ipv6AddressBox.Add_TextChanged({
    if ($script:updatingEndpoint) { return }
    try { Update-ControlEndpoint } catch { $script:hostStartButton.Enabled = $false }
})

$script:form.Add_FormClosing({ Stop-AllResources })
Add-UiLog "IPv6Mesh 中文 UI $Version 已启动。"
Add-UiLog "欢迎页提供创建网络和加入网络两条流程。"
Add-UiLog "关闭窗口时只清理本窗口启动的节点服务和控制面资源。"
if ($initialIPv6 -ne "") {
    Add-UiLog "已检测到可用的房主 IPv6。"
} else {
    Add-UiLog '未检测到可用的全局 IPv6；仍可选择“加入网络”并输入房主 IPv6。创建网络需要有效的房主 IPv6。' -Level '警告'
}
Set-PrimaryBusy $false ""
Show-WelcomePage
try {
    [void][System.Windows.Forms.Application]::Run($script:form)
} finally {
    Stop-AllResources
}
