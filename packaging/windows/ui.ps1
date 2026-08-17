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
    [string]$Version = "dev",
    [switch]$LayoutAudit
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
$script:contentPanel = $null
$script:operationShell = $null
$script:operationSplit = $null
$script:updatingSplitLayout = $false
$script:userSplitterDistance = -1
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

function Set-PageLayoutState {
    param([ValidateSet("Welcome", "Host", "Member")][string]$Name)
    $script:activePage = $Name
    if ($null -ne $script:welcomePanel) { $script:welcomePanel.Visible = ($Name -eq "Welcome") }
    if ($null -ne $script:operationShell) { $script:operationShell.Visible = ($Name -ne "Welcome") }
    if ($null -ne $script:hostPanel) { $script:hostPanel.Visible = ($Name -eq "Host") }
    if ($null -ne $script:memberPanel) { $script:memberPanel.Visible = ($Name -eq "Member") }
}

function Set-ResponsiveSplitLayout {
    if ($script:updatingSplitLayout -or $null -eq $script:operationSplit -or $script:operationSplit.IsDisposed) { return }
    if ($script:operationSplit.ClientSize.Height -le 0) { return }
    $script:updatingSplitLayout = $true
    try {
        $decision = Get-SplitLayoutDecision -AvailableHeight $script:operationSplit.ClientSize.Height -SplitterWidth $script:operationSplit.SplitterWidth -CurrentDistance $script:userSplitterDistance
        $script:operationSplit.SuspendLayout()
        try {
            $script:operationSplit.Panel1MinSize = 0
            $script:operationSplit.Panel2MinSize = 0
            $script:operationSplit.SplitterDistance = $decision.Distance
            $script:operationSplit.Panel1MinSize = $decision.UpperMinimum
            $script:operationSplit.Panel2MinSize = $decision.LowerMinimum
            $script:userSplitterDistance = $decision.Distance
        } finally {
            $script:operationSplit.ResumeLayout($true)
        }
    } catch {
        if ($LayoutAudit) { throw }
        return
    } finally {
        $script:updatingSplitLayout = $false
    }
}

function Set-ResponsiveWindowBounds {
    param([Parameter(Mandatory = $true)][System.Windows.Forms.Form]$Form)
    $workingArea = [System.Windows.Forms.Screen]::PrimaryScreen.WorkingArea
    $previousClientSize = $Form.ClientSize
    $Form.ClientSize = New-Object System.Drawing.Size(1120, 720)
    $preferredOuter = $Form.Size
    $Form.ClientSize = $previousClientSize
    $minimumWidth = [Math]::Min(900, $workingArea.Width)
    $minimumHeight = [Math]::Min(640, $workingArea.Height)
    $Form.MinimumSize = New-Object System.Drawing.Size($minimumWidth, $minimumHeight)
    $Form.Size = New-Object System.Drawing.Size(
        ([Math]::Min($preferredOuter.Width, $workingArea.Width)),
        ([Math]::Min($preferredOuter.Height, $workingArea.Height))
    )
}

function Show-Page {
    param([ValidateSet("Welcome", "Host", "Member")][string]$Name)
    Set-PageLayoutState $Name
    if ($Name -eq "Welcome") {
        Stop-StatusRefresh
    } else {
        Set-ResponsiveSplitLayout
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

function New-LayoutLabel {
    param([string]$Name, [string]$Text, [int]$FontSize = 9, [switch]$Bold)
    $label = New-Object System.Windows.Forms.Label
    $label.Name = $Name
    $label.Text = $Text
    $label.AutoSize = $true
    $label.Anchor = [System.Windows.Forms.AnchorStyles]::Left
    $label.Margin = New-Object System.Windows.Forms.Padding(6, 8, 6, 8)
    $style = if ($Bold) { [System.Drawing.FontStyle]::Bold } else { [System.Drawing.FontStyle]::Regular }
    $label.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", $FontSize, $style)
    return $label
}

function New-LayoutTextBox {
    param([string]$Name, [switch]$Password, [switch]$ReadOnly)
    $box = New-Object System.Windows.Forms.TextBox
    $box.Name = $Name
    $box.Dock = [System.Windows.Forms.DockStyle]::Fill
    $box.Margin = New-Object System.Windows.Forms.Padding(6)
    $box.UseSystemPasswordChar = $Password
    $box.ReadOnly = $ReadOnly
    return $box
}

function New-LayoutButton {
    param([string]$Name, [string]$Text, [int]$MinimumWidth = 100, [int]$MinimumHeight = 32)
    $button = New-Object System.Windows.Forms.Button
    $button.Name = $Name
    $button.Text = $Text
    $button.AutoSize = $true
    $button.AutoSizeMode = [System.Windows.Forms.AutoSizeMode]::GrowAndShrink
    $button.MinimumSize = New-Object System.Drawing.Size($MinimumWidth, $MinimumHeight)
    $button.Padding = New-Object System.Windows.Forms.Padding(10, 2, 10, 2)
    $button.Margin = New-Object System.Windows.Forms.Padding(6)
    $button.Anchor = [System.Windows.Forms.AnchorStyles]::Left
    return $button
}

function New-ResponsivePageGrid {
    param([string]$Name)
    $page = New-Object System.Windows.Forms.TableLayoutPanel
    $page.Name = $Name
    $page.Dock = [System.Windows.Forms.DockStyle]::Top
    $page.AutoSize = $true
    $page.AutoSizeMode = [System.Windows.Forms.AutoSizeMode]::GrowAndShrink
    $page.MinimumSize = New-Object System.Drawing.Size(820, 0)
    $page.Padding = New-Object System.Windows.Forms.Padding(20, 8, 20, 20)
    $page.ColumnCount = 3
    [void]$page.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Absolute, 130)))
    [void]$page.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))
    [void]$page.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Absolute, 150)))
    return $page
}

function Add-PageControl {
    param(
        [System.Windows.Forms.TableLayoutPanel]$Page,
        [System.Windows.Forms.Control]$Control,
        [int]$Column,
        [int]$Row,
        [int]$ColumnSpan = 1
    )
    [void]$Page.Controls.Add($Control, $Column, $Row)
    if ($ColumnSpan -gt 1) { $Page.SetColumnSpan($Control, $ColumnSpan) }
}

function Get-LeafControls {
    param([System.Windows.Forms.Control]$Root)
    foreach ($child in $Root.Controls) {
        if ($child.Controls.Count -eq 0) {
            Write-Output $child
        } else {
            Get-LeafControls $child
        }
    }
}

function Get-ClippedScreenRectangle {
    param(
        [System.Windows.Forms.Control]$Control,
        [System.Windows.Forms.Control]$Root
    )
    $rectangle = $Control.RectangleToScreen($Control.ClientRectangle)
    $ancestor = $Control.Parent
    while ($null -ne $ancestor) {
        $ancestorRectangle = $ancestor.RectangleToScreen($ancestor.ClientRectangle)
        $rectangle = [System.Drawing.Rectangle]::Intersect($rectangle, $ancestorRectangle)
        if ($ancestor -eq $Root) { break }
        $ancestor = $ancestor.Parent
    }
    return $rectangle
}

function Set-AuditFont {
    param([System.Windows.Forms.Control]$Root, [float]$Size)
    foreach ($child in $Root.Controls) {
        if ($child -eq $script:logBox) {
            $child.Font = New-Object System.Drawing.Font("Consolas", $Size)
        } else {
            $child.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", $Size)
        }
        if ($child.Controls.Count -gt 0) { Set-AuditFont $child $Size }
    }
}

function Invoke-ControlLayout {
    param([System.Windows.Forms.Control]$Root)
    $Root.PerformLayout()
    foreach ($child in $Root.Controls) {
        if ($child.Controls.Count -gt 0) { Invoke-ControlLayout $child }
    }
}

function Invoke-ResponsiveLayoutAudit {
    $errors = New-Object 'System.Collections.Generic.List[string]'
    $samples = New-Object 'System.Collections.Generic.List[object]'
    $cases = @(
        @{ Name = "preferred"; Width = 1120; Height = 720; Font = 9; Distance = -1 },
        @{ Name = "minimum"; Width = 900; Height = 640; Font = 9; Distance = -1 },
        @{ Name = "large"; Width = 1440; Height = 900; Font = 9; Distance = -1 },
        @{ Name = "constrained"; Width = 760; Height = 520; Font = 9; Distance = -1 },
        @{ Name = "large-font"; Width = 900; Height = 640; Font = 12; Distance = -1 },
        @{ Name = "upper-limit"; Width = 1120; Height = 720; Font = 9; Distance = 0 },
        @{ Name = "lower-limit"; Width = 1120; Height = 720; Font = 9; Distance = 100000 }
    )

    $workingArea = [System.Windows.Forms.Screen]::PrimaryScreen.WorkingArea
    if ($script:form.MinimumSize.Width -gt $workingArea.Width -or $script:form.MinimumSize.Height -gt $workingArea.Height) {
        [void]$errors.Add("configured minimum exceeds the screen working area")
    }
    if ($script:form.Width -gt $workingArea.Width -or $script:form.Height -gt $workingArea.Height) {
        [void]$errors.Add("initial window exceeds the screen working area")
    }

    $script:form.ShowInTaskbar = $false
    $script:form.StartPosition = [System.Windows.Forms.FormStartPosition]::Manual
    $script:form.Location = New-Object System.Drawing.Point(-32000, -32000)
    $script:form.Opacity = 0
    $script:form.Show()
    $script:form.MinimumSize = New-Object System.Drawing.Size(1, 1)

    foreach ($case in $cases) {
        Set-AuditFont $script:form ([float]$case.Font)
        foreach ($page in @("Welcome", "Host", "Member")) {
            if (($case.Name -in @("large-font", "upper-limit", "lower-limit")) -and $page -eq "Welcome") { continue }
            $script:form.ClientSize = New-Object System.Drawing.Size($case.Width, $case.Height)
            $script:userSplitterDistance = [int]$case.Distance
            Set-PageLayoutState $page
            Set-ResponsiveSplitLayout
            Invoke-ControlLayout $script:form
            [System.Windows.Forms.Application]::DoEvents()

            if ($script:statusRefreshTimer.Enabled) {
                [void]$errors.Add("$($case.Name)/$page started the status timer during audit")
            }
            $wantDiagnostics = $page -ne "Welcome"
            if ($script:operationShell.Visible -ne $wantDiagnostics) {
                [void]$errors.Add("$($case.Name)/$page operation-shell visibility mismatch")
            }
            if ($script:diagnosticsPanel.Visible -ne $wantDiagnostics) {
                [void]$errors.Add("$($case.Name)/$page diagnostics visibility mismatch")
            }

            if ($wantDiagnostics) {
                $upper = $script:operationSplit.Panel1.RectangleToScreen($script:operationSplit.Panel1.ClientRectangle)
                $lower = $script:operationSplit.Panel2.RectangleToScreen($script:operationSplit.Panel2.ClientRectangle)
                $intersection = [System.Drawing.Rectangle]::Intersect($upper, $lower)
                if (!$intersection.IsEmpty) {
                    [void]$errors.Add("$($case.Name)/$page split panels intersect")
                }
                $usable = [Math]::Max(0, $script:operationSplit.ClientSize.Height - $script:operationSplit.SplitterWidth)
                if (($script:operationSplit.Panel1MinSize + $script:operationSplit.Panel2MinSize) -gt $usable) {
                    [void]$errors.Add("$($case.Name)/$page split minima exceed usable height")
                }
                if ($script:operationSplit.SplitterDistance -lt $script:operationSplit.Panel1MinSize -or
                    $script:operationSplit.SplitterDistance -gt ($usable - $script:operationSplit.Panel2MinSize)) {
                    [void]$errors.Add("$($case.Name)/$page splitter lies outside minima")
                }
                if ($script:logBox.ClientSize.Width -le 0 -or $script:logBox.ClientSize.Height -le 0) {
                    [void]$errors.Add("$($case.Name)/$page log box has no usable area")
                }
            }

            $leaves = @(Get-LeafControls $script:form | Where-Object { $_.Visible -and $_.ClientSize.Width -gt 0 -and $_.ClientSize.Height -gt 0 })
            for ($leftIndex = 0; $leftIndex -lt $leaves.Count; $leftIndex++) {
                $left = Get-ClippedScreenRectangle $leaves[$leftIndex] $script:form
                if ($left.IsEmpty) { continue }
                for ($rightIndex = $leftIndex + 1; $rightIndex -lt $leaves.Count; $rightIndex++) {
                    $right = Get-ClippedScreenRectangle $leaves[$rightIndex] $script:form
                    if ($right.IsEmpty) { continue }
                    $leafIntersection = [System.Drawing.Rectangle]::Intersect($left, $right)
                    if ($leafIntersection.Width -gt 0 -and $leafIntersection.Height -gt 0) {
                        [void]$errors.Add("$($case.Name)/$page controls $($leaves[$leftIndex].Name) and $($leaves[$rightIndex].Name) overlap")
                    }
                }
            }

            $inputWidth = if ($page -eq "Host") { $script:ipv6AddressBox.ClientSize.Width } elseif ($page -eq "Member") { $script:memberHostIPv6Box.ClientSize.Width } else { 0 }
            [void]$samples.Add([pscustomobject]@{
                Case = $case.Name
                Page = $page
                InputWidth = $inputWidth
                LogWidth = if ($wantDiagnostics) { $script:logBox.ClientSize.Width } else { 0 }
                LogHeight = if ($wantDiagnostics) { $script:logBox.ClientSize.Height } else { 0 }
                SplitterDistance = if ($wantDiagnostics) { $script:operationSplit.SplitterDistance } else { 0 }
            })
        }
    }

    foreach ($page in @("Host", "Member")) {
        $minimum = $samples | Where-Object { $_.Case -eq "minimum" -and $_.Page -eq $page } | Select-Object -First 1
        $large = $samples | Where-Object { $_.Case -eq "large" -and $_.Page -eq $page } | Select-Object -First 1
        if ($large.InputWidth -le $minimum.InputWidth) { [void]$errors.Add("$page input did not grow") }
        if ($large.LogWidth -le $minimum.LogWidth) { [void]$errors.Add("$page log width did not grow") }
        if ($large.LogHeight -le $minimum.LogHeight) { [void]$errors.Add("$page log height did not grow") }
    }

    return [pscustomobject]@{
        Passed = $errors.Count -eq 0
        Errors = $errors.ToArray()
        Samples = $samples.ToArray()
    }
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
$script:form.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::Sizable
$script:form.AutoScaleMode = [System.Windows.Forms.AutoScaleMode]::Dpi
$script:form.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 9)
Set-ResponsiveWindowBounds $script:form

$rootLayout = New-Object System.Windows.Forms.TableLayoutPanel
$rootLayout.Name = "RootLayout"
$rootLayout.Dock = [System.Windows.Forms.DockStyle]::Fill
$rootLayout.ColumnCount = 1
$rootLayout.RowCount = 2
[void]$rootLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
[void]$rootLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 100)))
[void]$script:form.Controls.Add($rootLayout)

$headerLayout = New-Object System.Windows.Forms.TableLayoutPanel
$headerLayout.Name = "HeaderLayout"
$headerLayout.Dock = [System.Windows.Forms.DockStyle]::Fill
$headerLayout.AutoSize = $true
$headerLayout.Padding = New-Object System.Windows.Forms.Padding(20, 12, 20, 10)
$headerLayout.ColumnCount = 2
[void]$headerLayout.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::AutoSize)))
[void]$headerLayout.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))
[void]$rootLayout.Controls.Add($headerLayout, 0, 0)

$title = New-Object System.Windows.Forms.Label
$title.Name = "ProductTitle"
$title.Text = "IPv6Mesh 远程组网"
$title.AutoSize = $true
$title.Anchor = [System.Windows.Forms.AnchorStyles]::Left
$title.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 15, [System.Drawing.FontStyle]::Bold)
[void]$headerLayout.Controls.Add($title, 0, 0)

$script:statusLabel = New-Object System.Windows.Forms.Label
$script:statusLabel.Name = "HeaderStatus"
$script:statusLabel.Text = "等待选择"
$script:statusLabel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:statusLabel.AutoEllipsis = $true
$script:statusLabel.TextAlign = [System.Drawing.ContentAlignment]::MiddleRight
[void]$headerLayout.Controls.Add($script:statusLabel, 1, 0)

$script:contentPanel = New-Object System.Windows.Forms.Panel
$script:contentPanel.Name = "ContentPanel"
$script:contentPanel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:contentPanel.Padding = New-Object System.Windows.Forms.Padding(20, 8, 20, 20)
[void]$rootLayout.Controls.Add($script:contentPanel, 0, 1)

$script:welcomePanel = New-Object System.Windows.Forms.TableLayoutPanel
$script:welcomePanel.Name = "WelcomePanel"
$script:welcomePanel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:welcomePanel.ColumnCount = 4
$script:welcomePanel.RowCount = 5
[void]$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
[void]$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
[void]$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
[void]$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
[void]$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 35)))
[void]$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
[void]$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
[void]$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Absolute, 100)))
[void]$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 65)))
[void]$script:contentPanel.Controls.Add($script:welcomePanel)

$welcomeTitle = New-Object System.Windows.Forms.Label
$welcomeTitle.Name = "WelcomeTitle"
$welcomeTitle.Text = "你想做什么？"
$welcomeTitle.AutoSize = $true
$welcomeTitle.Anchor = [System.Windows.Forms.AnchorStyles]::None
$welcomeTitle.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 22)
[void]$script:welcomePanel.Controls.Add($welcomeTitle, 1, 1)
$script:welcomePanel.SetColumnSpan($welcomeTitle, 2)

$welcomeHelp = New-Object System.Windows.Forms.Label
$welcomeHelp.Name = "WelcomeHelp"
$welcomeHelp.Text = "选择一种方式开始 IPv6Mesh 房间流程。"
$welcomeHelp.AutoSize = $true
$welcomeHelp.Anchor = [System.Windows.Forms.AnchorStyles]::None
[void]$script:welcomePanel.Controls.Add($welcomeHelp, 1, 2)
$script:welcomePanel.SetColumnSpan($welcomeHelp, 2)

$createButton = New-Object System.Windows.Forms.Button
$createButton.Name = "WelcomeCreate"
$createButton.Text = "创建网络"
$createButton.Dock = [System.Windows.Forms.DockStyle]::Fill
$createButton.Margin = New-Object System.Windows.Forms.Padding(10, 15, 10, 15)
$createButton.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 12)
$createButton.Add_Click({ Show-HostPage })
[void]$script:welcomePanel.Controls.Add($createButton, 1, 3)

$joinButton = New-Object System.Windows.Forms.Button
$joinButton.Name = "WelcomeJoin"
$joinButton.Text = "加入网络"
$joinButton.Dock = [System.Windows.Forms.DockStyle]::Fill
$joinButton.Margin = New-Object System.Windows.Forms.Padding(10, 15, 10, 15)
$joinButton.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 12)
$joinButton.Add_Click({ Show-MemberPage })
[void]$script:welcomePanel.Controls.Add($joinButton, 2, 3)

$script:operationShell = New-Object System.Windows.Forms.Panel
$script:operationShell.Name = "OperationShell"
$script:operationShell.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:operationShell.Visible = $false
[void]$script:contentPanel.Controls.Add($script:operationShell)

$script:operationSplit = New-Object System.Windows.Forms.SplitContainer
$script:operationSplit.Name = "OperationSplit"
$script:operationSplit.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:operationSplit.Orientation = [System.Windows.Forms.Orientation]::Horizontal
$script:operationSplit.FixedPanel = [System.Windows.Forms.FixedPanel]::Panel1
$script:operationSplit.SplitterWidth = 6
$script:operationSplit.Panel1.AutoScroll = $true
[void]$script:operationShell.Controls.Add($script:operationSplit)
$script:operationSplit.Add_SplitterMoved({
    if (!$script:updatingSplitLayout) {
        $script:userSplitterDistance = $script:operationSplit.SplitterDistance
        Set-ResponsiveSplitLayout
    }
})
$script:operationSplit.Add_SizeChanged({ Set-ResponsiveSplitLayout })
$script:form.Add_Shown({ Set-ResponsiveSplitLayout })

$script:hostPanel = New-ResponsivePageGrid "HostPanel"
$script:hostPanel.RowCount = 6
for ($row = 0; $row -lt 6; $row++) {
    [void]$script:hostPanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
}
[void]$script:operationSplit.Panel1.Controls.Add($script:hostPanel)

$hostBackButton = New-LayoutButton "HostBack" "返回" 90
$hostBackButton.Add_Click({ Show-WelcomePage })
$script:backButtons += $hostBackButton
Add-PageControl $script:hostPanel $hostBackButton 0 0

$hostTitle = New-LayoutLabel "HostTitle" "创建网络" 18
Add-PageControl $script:hostPanel $hostTitle 1 0 2

Add-PageControl $script:hostPanel (New-LayoutLabel "HostIPv6Label" "房主 IPv6：") 0 1
$script:ipv6AddressBox = New-LayoutTextBox "HostIPv6Input"
$script:ipv6AddressBox.Text = $initialIPv6
$script:ipv6AddressBox.Dock = [System.Windows.Forms.DockStyle]::Fill
Add-PageControl $script:hostPanel $script:ipv6AddressBox 1 1
$detectButton = New-LayoutButton "HostDetect" "重新检测" 120
$detectButton.Add_Click({ $null = Refresh-LocalIPv6 })
Add-PageControl $script:hostPanel $detectButton 2 1

$hostIPv6Help = New-LayoutLabel "HostIPv6Help" "房主 IPv6 仅接受首选、非 SkipAsSource 的 2000::/3 全局地址。"
Add-PageControl $script:hostPanel $hostIPv6Help 1 2 2

Add-PageControl $script:hostPanel (New-LayoutLabel "ControlURLLabel" "控制面地址：") 0 3
$script:controlUrlBox = New-LayoutTextBox "ControlURL" -ReadOnly
Add-PageControl $script:hostPanel $script:controlUrlBox 1 3
$copyHostIPv6Button = New-LayoutButton "CopyHostIPv6" "复制房主 IPv6" 140
$copyHostIPv6Button.Add_Click({ Copy-UiField $script:ipv6AddressBox "房主 IPv6" })
Add-PageControl $script:hostPanel $copyHostIPv6Button 2 3

$script:hostVirtualIPv4Label = New-LayoutLabel "HostVirtualIPv4" "房主虚拟 IPv4：未加入" 12 -Bold
Add-PageControl $script:hostPanel $script:hostVirtualIPv4Label 0 4 3

$script:hostStartButton = New-LayoutButton "HostStart" "创建并连接" 190 44
$script:hostStartButton.Add_Click({ Start-HostRoom })
Add-PageControl $script:hostPanel $script:hostStartButton 0 5 3

$script:memberPanel = New-ResponsivePageGrid "MemberPanel"
$script:memberPanel.RowCount = 6
for ($row = 0; $row -lt 6; $row++) {
    [void]$script:memberPanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
}
$script:memberPanel.Visible = $false
[void]$script:operationSplit.Panel1.Controls.Add($script:memberPanel)

$memberBackButton = New-LayoutButton "MemberBack" "返回" 90
$memberBackButton.Add_Click({ Show-WelcomePage })
$script:backButtons += $memberBackButton
Add-PageControl $script:memberPanel $memberBackButton 0 0

$memberTitle = New-LayoutLabel "MemberTitle" "加入网络" 18
Add-PageControl $script:memberPanel $memberTitle 1 0 2

Add-PageControl $script:memberPanel (New-LayoutLabel "MemberHostIPv6Label" "房主 IPv6：") 0 1
$script:memberHostIPv6Box = New-LayoutTextBox "MemberHostIPv6Input"
$script:memberHostIPv6Box.Dock = [System.Windows.Forms.DockStyle]::Fill
Add-PageControl $script:memberPanel $script:memberHostIPv6Box 1 1 2

$memberIPv6Help = New-LayoutLabel "MemberIPv6Help" "成员只需输入房主 IPv6；地址必须是 2000::/3 全局 IPv6。"
Add-PageControl $script:memberPanel $memberIPv6Help 1 2 2

Add-PageControl $script:memberPanel (New-LayoutLabel "MemberNameCaption" "本机名称：") 0 3
$script:memberNameLabel = New-LayoutLabel "MemberName" ([string]$env:COMPUTERNAME)
Add-PageControl $script:memberPanel $script:memberNameLabel 1 3 2

$script:memberVirtualIPv4Label = New-LayoutLabel "MemberVirtualIPv4" "本机虚拟 IPv4：未加入" 12 -Bold
Add-PageControl $script:memberPanel $script:memberVirtualIPv4Label 0 4 3

$script:memberJoinButton = New-LayoutButton "MemberJoin" "加入并连接" 190 44
$script:memberJoinButton.Add_Click({ Join-MemberRoom })
Add-PageControl $script:memberPanel $script:memberJoinButton 0 5 3

$script:diagnosticsPanel = New-Object System.Windows.Forms.GroupBox
$script:diagnosticsPanel.Name = "DiagnosticsPanel"
$script:diagnosticsPanel.Text = "诊断与日志"
$script:diagnosticsPanel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:diagnosticsPanel.Visible = $true
$script:diagnosticsPanel.Padding = New-Object System.Windows.Forms.Padding(12, 8, 12, 12)
[void]$script:operationSplit.Panel2.Controls.Add($script:diagnosticsPanel)

$diagnosticsLayout = New-Object System.Windows.Forms.TableLayoutPanel
$diagnosticsLayout.Name = "DiagnosticsLayout"
$diagnosticsLayout.Dock = [System.Windows.Forms.DockStyle]::Fill
$diagnosticsLayout.AutoSize = $false
$diagnosticsLayout.MinimumSize = New-Object System.Drawing.Size(200, 220)
$diagnosticsLayout.ColumnCount = 1
$diagnosticsLayout.RowCount = 4
[void]$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
[void]$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
[void]$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 100)))
[void]$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
$diagnosticsViewport = New-Object System.Windows.Forms.Panel
$diagnosticsViewport.Name = "DiagnosticsViewport"
$diagnosticsViewport.Dock = [System.Windows.Forms.DockStyle]::Fill
$diagnosticsViewport.AutoScroll = $true
[void]$script:diagnosticsPanel.Controls.Add($diagnosticsViewport)
[void]$diagnosticsViewport.Controls.Add($diagnosticsLayout)

$script:nodeStatusLabel = New-LayoutLabel "NodeStatus" "节点服务未检查状态"
$script:nodeStatusLabel.AutoSize = $false
$script:nodeStatusLabel.MinimumSize = New-Object System.Drawing.Size(0, 28)
$script:nodeStatusLabel.AutoEllipsis = $true
$script:nodeStatusLabel.Dock = [System.Windows.Forms.DockStyle]::Fill
[void]$diagnosticsLayout.Controls.Add($script:nodeStatusLabel, 0, 0)

$statusActions = New-Object System.Windows.Forms.FlowLayoutPanel
$statusActions.Name = "StatusActions"
$statusActions.Dock = [System.Windows.Forms.DockStyle]::Fill
$statusActions.AutoSize = $true
$statusActions.WrapContents = $true
$statusActions.FlowDirection = [System.Windows.Forms.FlowDirection]::LeftToRight
[void]$diagnosticsLayout.Controls.Add($statusActions, 0, 1)

$refreshStatusButton = New-LayoutButton "RefreshStatus" "刷新状态" 100
$refreshStatusButton.Add_Click({ $null = Get-NodeStatus })
[void]$statusActions.Controls.Add($refreshStatusButton)
$connectButton = New-LayoutButton "ConnectNode" "连接" 90
$connectButton.Add_Click({ Connect-Node })
[void]$statusActions.Controls.Add($connectButton)
$disconnectButton = New-LayoutButton "DisconnectNode" "断开" 90
$disconnectButton.Add_Click({ Disconnect-Node })
[void]$statusActions.Controls.Add($disconnectButton)
$leaveButton = New-LayoutButton "LeaveRoom" "离开房间" 110
$leaveButton.Add_Click({ Leave-Node })
[void]$statusActions.Controls.Add($leaveButton)

$script:logBox = New-Object System.Windows.Forms.TextBox
$script:logBox.Name = "LogBox"
$script:logBox.Multiline = $true
$script:logBox.ReadOnly = $true
$script:logBox.ScrollBars = [System.Windows.Forms.ScrollBars]::Both
$script:logBox.WordWrap = $false
$script:logBox.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:logBox.MinimumSize = New-Object System.Drawing.Size(200, 80)
$script:logBox.Margin = New-Object System.Windows.Forms.Padding(6)
$script:logBox.Font = New-Object System.Drawing.Font("Consolas", 9)
[void]$diagnosticsLayout.Controls.Add($script:logBox, 0, 2)

$logActions = New-Object System.Windows.Forms.FlowLayoutPanel
$logActions.Name = "LogActions"
$logActions.Dock = [System.Windows.Forms.DockStyle]::Fill
$logActions.AutoSize = $true
$logActions.WrapContents = $true
$logActions.FlowDirection = [System.Windows.Forms.FlowDirection]::LeftToRight
[void]$diagnosticsLayout.Controls.Add($logActions, 0, 3)

$clearLogButton = New-LayoutButton "ClearLog" "清空日志" 100
$clearLogButton.Add_Click({ $script:logLines.Clear(); $script:logBox.Clear() })
[void]$logActions.Controls.Add($clearLogButton)
$copyLogButton = New-LayoutButton "CopyLog" "复制日志" 100
$copyLogButton.Add_Click({ [System.Windows.Forms.Clipboard]::SetText(($script:logLines -join [Environment]::NewLine)) })
[void]$logActions.Controls.Add($copyLogButton)
$exportLogButton = New-LayoutButton "ExportLog" "导出日志" 100
$exportLogButton.Add_Click({ Export-UiLog })
[void]$logActions.Controls.Add($exportLogButton)

$script:statusRefreshTimer = New-Object System.Windows.Forms.Timer
$script:statusRefreshTimer.Interval = 2000
$script:statusRefreshTimer.Add_Tick({ Invoke-AutomaticStatusRefresh })

$script:ipv6AddressBox.Add_TextChanged({
    if ($script:updatingEndpoint) { return }
    try { Update-ControlEndpoint } catch { $script:hostStartButton.Enabled = $false }
})

$script:form.Add_FormClosing({ Stop-AllResources })
if ($LayoutAudit) {
    try {
        $audit = Invoke-ResponsiveLayoutAudit
        $audit | ConvertTo-Json -Depth 6 -Compress
        if (!$audit.Passed) { exit 1 }
    } finally {
        Stop-StatusRefresh
        if ($null -ne $script:form -and !$script:form.IsDisposed) {
            $script:form.Hide()
            $script:form.Dispose()
        }
    }
    return
}
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
