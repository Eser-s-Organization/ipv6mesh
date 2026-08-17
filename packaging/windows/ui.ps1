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
    [switch]$LayoutAudit,
    [switch]$AsyncPollingAudit
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
$script:hostPageShell = $null
$script:memberPageShell = $null
$script:hostMemberPanel = $null
$script:memberMemberPanel = $null
$script:hostMemberGrid = $null
$script:memberMemberGrid = $null
$script:hostMemberCountLabel = $null
$script:memberMemberCountLabel = $null
$script:hostMemberRefreshLabel = $null
$script:memberMemberRefreshLabel = $null
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
$script:automaticPollingOperation = $null
$script:automaticPollingGeneration = 0
$script:automaticPollingApplyPending = $false
$script:automaticPollingPendingResult = $null
$script:automaticPollingPendingGeneration = 0
$script:asyncPollingAudit = [bool]$AsyncPollingAudit
$script:asyncPollingAuditCounters = @{
    WorkerStarts = 0
    ActiveWorkers = 0
    MaxConcurrentWorkers = 0
    MessagePumpTicks = 0
    SlowResultApplied = $false
    LateResultWrites = 0
    UiThreadApplied = $false
    MembersRetainedOnFail = $false
    PollingTicks = 0
    LastOperationState = ''
}
$script:asyncPollingAuditUiThreadId = 0
$script:asyncPollingAuditOwnerThreadId = 0
$script:asyncPollingAuditInitialMemberRows = 0
$script:asyncPollingAuditStage = ''
$script:asyncPollingAuditResponsiveTicks = 0
$script:asyncPollingAuditLateQueued = $false
$script:hasStatusRefreshResult = $false
$script:lastStatusRefreshSucceeded = $false
$script:lastStatusFingerprint = ""
$script:hasMemberRefreshResult = $false
$script:lastMemberRefreshSucceeded = $false
$script:lastMemberFingerprint = ""
$script:memberRefreshInProgress = $false
$script:updatingMemberLayout = $false
$script:uiMutex = $null
$script:ownsUiMutex = $false
$script:uiInstanceActive = $false
$script:uiFlowState = "Idle"
$script:uiFlowStates = @("Idle", "HostSetup", "MemberSetup", "PreparingHost", "PreparingMember", "Hosting", "JoinedMember", "Cleaning")
$script:welcomeCreateButton = $null
$script:welcomeJoinButton = $null
$script:refreshStatusButton = $null
$script:connectButton = $null
$script:disconnectButton = $null
$script:leaveButton = $null

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
    $update = {
        [void]$script:logLines.Add($line)
        if ($null -eq $script:logBox -or $script:logBox.IsDisposed) { return }
        $script:logBox.AppendText($line + [Environment]::NewLine)
        $script:logBox.SelectionStart = $script:logBox.TextLength
        $script:logBox.ScrollToCaret()
    }.GetNewClosure()
    if ($null -ne $script:logBox -and !$script:logBox.IsDisposed -and $script:logBox.IsHandleCreated) {
        try {
            [void]$script:logBox.BeginInvoke([System.Windows.Forms.MethodInvoker]$update)
            return
        } catch {
        }
    }
    $update.Invoke()
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

function Get-MemberLogDecision {
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

function Get-MemberLayoutMode {
    param(
        [int]$AvailableWidth,
        [int]$SettingsPreferredWidth,
        [int]$MembersMinimumWidth,
        [int]$Gap
    )
    $required = [Math]::Max(0, $SettingsPreferredWidth) + [Math]::Max(0, $MembersMinimumWidth) + [Math]::Max(0, $Gap)
    if ([Math]::Max(0, $AvailableWidth) -ge $required) { return "Wide" }
    return "Narrow"
}

function Test-UiFlowTransition {
    param([Parameter(Mandatory = $true)][string]$From, [Parameter(Mandatory = $true)][string]$To)
    $allowed = @{
        "Idle->HostSetup" = $true
        "Idle->MemberSetup" = $true
        "HostSetup->Idle" = $true
        "HostSetup->PreparingHost" = $true
        "MemberSetup->Idle" = $true
        "MemberSetup->PreparingMember" = $true
        "PreparingHost->Hosting" = $true
        "PreparingHost->Cleaning" = $true
        "PreparingMember->JoinedMember" = $true
        "PreparingMember->Cleaning" = $true
        "Hosting->Cleaning" = $true
        "JoinedMember->Cleaning" = $true
        "Cleaning->Idle" = $true
        "Cleaning->HostSetup" = $true
        "Cleaning->MemberSetup" = $true
    }
    return $allowed.ContainsKey("$From->$To")
}

function Update-UiFlowControls {
    $state = [string]$script:uiFlowState
    $setupHost = $state -eq "HostSetup"
    $setupMember = $state -eq "MemberSetup"
    $preparing = $state -in @("PreparingHost", "PreparingMember", "Cleaning")
    $active = $state -in @("Hosting", "JoinedMember")
    if ($null -ne $script:hostStartButton -and !$script:hostStartButton.IsDisposed) {
        $hostReady = $false
        if ($null -ne $script:ipv6AddressBox) { $hostReady = Test-GlobalIPv6 (Get-BoxText $script:ipv6AddressBox) }
        $script:hostStartButton.Enabled = $setupHost -and !$script:primaryBusy -and $hostReady
    }
    if ($null -ne $script:memberJoinButton -and !$script:memberJoinButton.IsDisposed) {
        $script:memberJoinButton.Enabled = $setupMember -and !$script:primaryBusy
    }
    foreach ($button in $script:backButtons) {
        if ($null -ne $button -and !$button.IsDisposed) { $button.Enabled = ($setupHost -or $setupMember) -and !$script:primaryBusy }
    }
    if ($null -ne $script:leaveButton -and !$script:leaveButton.IsDisposed) {
        $script:leaveButton.Text = if ($state -eq "Hosting") { "结束房间" } else { "离开房间" }
        $script:leaveButton.Enabled = $active -and !$script:primaryBusy
    }
    foreach ($button in @($script:refreshStatusButton, $script:connectButton, $script:disconnectButton)) {
        if ($null -ne $button -and !$button.IsDisposed) { $button.Enabled = $active -and !$preparing }
    }
    if ($null -ne $script:welcomeCreateButton -and !$script:welcomeCreateButton.IsDisposed) { $script:welcomeCreateButton.Enabled = !$preparing -and $state -eq "Idle" }
    if ($null -ne $script:welcomeJoinButton -and !$script:welcomeJoinButton.IsDisposed) { $script:welcomeJoinButton.Enabled = !$preparing -and $state -eq "Idle" }
}

function Set-UiFlowState {
    param([Parameter(Mandatory = $true)][string]$To)
    if ($script:uiFlowState -eq $To) {
        Update-UiFlowControls
        return $true
    }
    if (!(Test-UiFlowTransition -From $script:uiFlowState -To $To)) {
        Add-UiLog "已拒绝非法界面模式切换。" "警告"
        return $false
    }
    $script:uiFlowState = $To
    Update-UiFlowControls
    return $true
}

function Enter-UiInstance {
    $createdNew = $false
    try {
        $script:uiMutex = New-Object System.Threading.Mutex($true, "Global\IPv6Mesh.WindowsUI", [ref]$createdNew)
        if (!$createdNew) {
            try { $script:ownsUiMutex = $script:uiMutex.WaitOne(0) } catch [System.Threading.AbandonedMutexException] { $script:ownsUiMutex = $true }
        } else {
            $script:ownsUiMutex = $true
        }
        return $script:ownsUiMutex
    } catch {
        if ($null -ne $script:uiMutex) { $script:uiMutex.Dispose(); $script:uiMutex = $null }
        $script:ownsUiMutex = $false
        return $false
    }
}

function Exit-UiInstance {
    if ($script:ownsUiMutex -and $null -ne $script:uiMutex) {
        try { $script:uiMutex.ReleaseMutex() } catch {}
    }
    $script:ownsUiMutex = $false
    if ($null -ne $script:uiMutex) { $script:uiMutex.Dispose(); $script:uiMutex = $null }
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
        Update-UiFlowControls
        Add-UiLog "已检测本机全局 IPv6；房主控制面地址已准备。"
        Set-UiStatus "房主 IPv6 已就绪" ([System.Drawing.Color]::ForestGreen)
        return $true
    } catch {
        Update-UiFlowControls
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
        if ($stderr -match '(room_not_ready|room_mode_disabled|node_already_joined|room_full|join_rate_limited|enrollment_recovery_pending|invalid_node|request_too_large|unauthorized|control_unreachable|operation_timeout)') {
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
            control_unreachable = "房主控制面不可访问，请确认房主窗口仍在运行且 TCP 8080 可达。"
            operation_timeout = "操作等待超时，请检查网络后重试。"
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

function Assert-MemberControlReady {
    if (!(Test-ControlHealth -Quiet)) {
        throw '房主控制面不可访问，请确认房主窗口仍在运行且 TCP 8080 可达。'
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

function Clear-ActiveRoomState {
    $script:activeNetworkId = ""
    Clear-RoomMembers
    if ($null -ne $script:hostVirtualIPv4Label -and !$script:hostVirtualIPv4Label.IsDisposed) { $script:hostVirtualIPv4Label.Text = "房主虚拟 IPv4：未加入" }
    if ($null -ne $script:memberVirtualIPv4Label -and !$script:memberVirtualIPv4Label.IsDisposed) { $script:memberVirtualIPv4Label.Text = "本机虚拟 IPv4：未加入" }
    if ($null -ne $script:nodeStatusLabel -and !$script:nodeStatusLabel.IsDisposed) { $script:nodeStatusLabel.Text = "本机尚未加入房间" }
    Set-UiStatus "节点未加入房间" ([System.Drawing.Color]::DarkOrange)
}

function Stop-FailedPreparation {
    param([Parameter(Mandatory = $true)][ValidateSet("HostSetup", "MemberSetup")][string]$ReturnState)
    if (![string]::IsNullOrWhiteSpace($script:activeNetworkId)) {
        try {
            $result = Invoke-VpnCtl -Arguments @("leave", "--network", $script:activeNetworkId) -SuppressStandardOutput -Quiet
            if ($result.ExitCode -ne 0) { Add-UiLog "准备失败后的房间清理未完成。" "警告" }
        } catch {
            Add-UiLog "准备失败后的房间清理未完成。" "警告"
        }
    }
    Clear-ActiveRoomState
    Stop-StartedResources
    if ($script:uiFlowState -in @("PreparingHost", "PreparingMember")) {
        [void](Set-UiFlowState "Cleaning")
        [void](Set-UiFlowState $ReturnState)
    }
}

function Exit-ActiveRoom {
    param([switch]$ShuttingDown)
    if ($script:uiFlowState -notin @("Hosting", "JoinedMember")) { return $false }
    if (!(Set-UiFlowState "Cleaning")) { return $false }
    $success = $true
    try {
        if (![string]::IsNullOrWhiteSpace($script:activeNetworkId)) {
            try {
                $result = Invoke-VpnCtl -Arguments @("leave", "--network", $script:activeNetworkId) -SuppressStandardOutput -Quiet
                if ($result.ExitCode -ne 0) { throw "leave failed" }
                $null = Convert-ResultToJson $result "离开房间" -Quiet
            } catch {
                $success = $false
                Add-UiLog "离开房间失败，已继续释放本机资源。" "错误"
            }
        }
    } finally {
        Clear-ActiveRoomState
        Stop-StartedResources
    }
    if (!$ShuttingDown) {
        [void](Set-UiFlowState "Idle")
        Show-WelcomePage
    }
    return $success
}

function Stop-AllResources {
    if ($script:cleanupStarted) { return }
    $script:cleanupStarted = $true
    Add-UiLog "正在执行退出清理。"
    Dispose-StatusRefreshTimer
    $null = Exit-ActiveRoom -ShuttingDown
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

function Apply-NodeStatusResult {
    param(
        [Parameter(Mandatory = $true)]$Result,
        [switch]$Automatic
    )
    try {
        $status = Convert-ResultToJson $Result "读取节点状态" -Quiet:$Automatic
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
    }
}

function Get-NodeStatus {
	param([switch]$Automatic)
	if ($script:statusRefreshInProgress) { return $null }
	$script:statusRefreshInProgress = $true
    try {
        $result = Invoke-VpnCtl -Arguments @("status") -SuppressStandardOutput -Quiet:$Automatic
        return Apply-NodeStatusResult $result -Automatic:$Automatic
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

function Apply-RoomMembersResult {
    param(
        [Parameter(Mandatory = $true)]$Result,
        [switch]$Automatic
    )
    try {
        $response = Convert-ResultToJson $Result "读取房间成员" -Quiet:$Automatic
        $members = @($response.members)
        $fingerprint = (($members | ForEach-Object { "{0}|{1}|{2}|{3}" -f $_.display_name, $_.virtual_ipv4, $_.is_local, $_.state }) -join ";")
        Set-RoomMemberRows $members
        foreach ($label in @($script:hostMemberRefreshLabel, $script:memberMemberRefreshLabel)) {
            if ($null -ne $label -and !$label.IsDisposed) { $label.Text = "已更新：$($members.Count) 名在线" }
        }
        $decision = Get-MemberLogDecision -Automatic ([bool]$Automatic) -Succeeded $true -Fingerprint $fingerprint -HasPrevious $script:hasMemberRefreshResult -PreviousSucceeded $script:lastMemberRefreshSucceeded -PreviousFingerprint $script:lastMemberFingerprint
        if ($decision -eq "Recovered") {
            Add-UiLog "房间成员读取已恢复：$($members.Count) 名在线。"
        } elseif ($decision -eq "Changed" -or $decision -eq "Manual") {
            Add-UiLog "房间成员列表已更新：$($members.Count) 名在线。"
        }
        $script:hasMemberRefreshResult = $true
        $script:lastMemberRefreshSucceeded = $true
        $script:lastMemberFingerprint = $fingerprint
        return $members
    } catch {
        foreach ($label in @($script:hostMemberRefreshLabel, $script:memberMemberRefreshLabel)) {
            if ($null -ne $label -and !$label.IsDisposed) { $label.Text = "成员读取失败，保留上次列表" }
        }
        $decision = Get-MemberLogDecision -Automatic ([bool]$Automatic) -Succeeded $false -Fingerprint "" -HasPrevious $script:hasMemberRefreshResult -PreviousSucceeded $script:lastMemberRefreshSucceeded -PreviousFingerprint $script:lastMemberFingerprint
        if ($decision -eq "Failed" -or $decision -eq "Manual") { Add-UiLog "读取房间成员失败，保留上次列表。" "警告" }
        $script:hasMemberRefreshResult = $true
        $script:lastMemberRefreshSucceeded = $false
        return $null
    }
}

function Get-RoomMembers {
    param([switch]$Automatic)
    if ($script:memberRefreshInProgress -or [string]::IsNullOrWhiteSpace($script:activeNetworkId)) { return $null }
    if ($script:uiFlowState -notin @("Hosting", "JoinedMember")) { return $null }
    $script:memberRefreshInProgress = $true
    try {
        $result = Invoke-VpnCtl -Arguments @("room", "members") -SuppressStandardOutput -Quiet:$Automatic
        return Apply-RoomMembersResult $result -Automatic:$Automatic
    } finally {
        $script:memberRefreshInProgress = $false
    }
}

function Start-HostRoom {
    if ($script:uiFlowState -ne "HostSetup") { Add-UiLog "创建房间操作已被忽略。" "警告"; return }
    if (!(Set-UiFlowState 'PreparingHost')) { return }
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
        [void](Set-UiFlowState 'Hosting')
        $null = Get-NodeStatus
        $null = Get-RoomMembers
        Set-UiStatus "房主已连接" ([System.Drawing.Color]::ForestGreen)
        Add-UiLog "房间创建完成；可将房主 IPv6 提供给成员。"
    } catch {
        Add-UiLog "创建房间失败，正在清理本次启动的资源。" "错误"
        Stop-FailedPreparation "HostSetup"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, "创建房间失败，请查看诊断日志。", "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    } finally {
        Set-PrimaryBusy $false ""
    }
}

function Join-MemberRoom {
    if ($script:uiFlowState -ne "MemberSetup") { Add-UiLog "加入房间操作已被忽略。" "警告"; return }
    if (!(Set-UiFlowState 'PreparingMember')) { return }
    Set-PrimaryBusy $true "正在加入房间并连接本机……"
    try {
        $hostIPv6 = Get-BoxText $script:memberHostIPv6Box
        if ($hostIPv6 -eq "") { throw "请输入房主 IPv6。" }
        $endpoint = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "endpoint", "--host-ipv6", $hostIPv6) -SuppressStandardOutput) "验证房主 IPv6"
        $script:controlUrl = [string]$endpoint.control_url
        $script:controlUrlBox.Text = $script:controlUrl
        Assert-MemberControlReady
        if (!(Install-NodeService -ControlUrl $script:controlUrl)) { throw "节点服务安装失败。" }
        $joined = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "join", "--host-ipv6", $hostIPv6, "--name", $env:COMPUTERNAME) -SuppressStandardOutput) "加入房间"
        $script:activeNetworkId = [string]$joined.network_id
        Set-ActiveVirtualIPv4 $joined "Member"
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", $script:activeNetworkId) -SuppressStandardOutput) "连接虚拟网络"
        [void](Set-UiFlowState 'JoinedMember')
        $null = Get-NodeStatus
        $null = Get-RoomMembers
        Set-UiStatus "成员已连接" ([System.Drawing.Color]::ForestGreen)
        Add-UiLog "已加入房间并连接本机。"
    } catch {
        Add-UiLog "加入房间失败，正在清理本次启动的资源。" "错误"
        Stop-FailedPreparation "MemberSetup"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, "加入房间失败，请查看诊断日志。", "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
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
    Update-UiFlowControls
}

function Invoke-UiBeginInvoke {
    param([Parameter(Mandatory = $true)][scriptblock]$Callback)
    $form = $script:form
    if ($null -eq $form -or $form.IsDisposed -or $form.Disposing -or !$form.IsHandleCreated) { return $false }
    try {
        [void]$form.BeginInvoke([System.Windows.Forms.MethodInvoker]$Callback)
        return $true
    } catch {
        return $false
    }
}

function Start-AutomaticPollingCommand {
    param(
        [Parameter(Mandatory = $true)][hashtable]$State,
        [Parameter(Mandatory = $true)][ValidateSet("Status", "Members")][string]$Phase
    )
    $fileName = Get-PayloadExecutable "vpnctl.exe"
    $arguments = if ($Phase -eq "Status") { @("status") } else { @("room", "members") }
    if ($script:asyncPollingAudit) {
        $fileName = Get-PowerShellPath
        if ($Phase -eq "Status") {
            $auditStatus = '{"network_id":"audit-network","virtual_ipv4":"10.42.0.2","path_state":"direct","last_error":""}'
            $auditCommand = "Start-Sleep -Milliseconds 5200; [Console]::Out.Write('$auditStatus')"
            $encodedCommand = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($auditCommand))
            $arguments = @("-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", $encodedCommand)
        } else {
            $auditCommand = "[Console]::Error.Write('control_unreachable'); exit 1"
            $encodedCommand = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($auditCommand))
            $arguments = @("-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", $encodedCommand)
        }
    }
    $environment = Get-ClientEnvironment
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $fileName
    $psi.Arguments = (($arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join " ")
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($key in $environment.Keys) {
        $psi.EnvironmentVariables[$key] = [string]$environment[$key]
    }
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $psi
    $stdoutTask = $null
    $stderrTask = $null
    try {
        [void]$process.Start()
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $State["${Phase}StdoutTask"] = $stdoutTask
        $State["${Phase}StderrTask"] = $stderrTask
        $State["Process"] = $process
        $process.EnableRaisingEvents = $true
    } catch {
        try {
            if (!$process.HasExited) { $process.Kill() }
        } catch {}
        try { $process.Dispose() } catch {}
        throw
    }
}

function Complete-AutomaticPollingCommand {
    param(
        [Parameter(Mandatory = $true)][hashtable]$State,
        [Parameter(Mandatory = $true)][ValidateSet("Status", "Members")][string]$Phase
    )
    if ([bool]$State["Cancelled"]) { return $false }
    $process = $State["Process"]
    if ($null -eq $process) { return $false }
    try { $exited = $process.HasExited } catch { $exited = $true }
    if (!$exited) { return $false }
    $stdoutTask = $State["${Phase}StdoutTask"]
    $stderrTask = $State["${Phase}StderrTask"]
    if ($null -ne $stdoutTask -and !$stdoutTask.IsCompleted) { return $false }
    if ($null -ne $stderrTask -and !$stderrTask.IsCompleted) { return $false }
    try {
        $stdout = if ($null -ne $stdoutTask) { [string]$stdoutTask.Result } else { "" }
        $stderr = if ($null -ne $stderrTask) { [string]$stderrTask.Result } else { "" }
        $State["${Phase}Result"] = [pscustomobject]@{ ExitCode = $process.ExitCode; Stdout = $stdout; Stderr = $stderr }
    } catch {
        $State["${Phase}Result"] = [pscustomobject]@{ ExitCode = 1; Stdout = ""; Stderr = "" }
    } finally {
        try { $process.Dispose() } catch {}
        $State["Process"] = $null
    }
    $State["Phase"] = "${Phase}Done"
    return $true
}

function Start-AutomaticPollingOperation {
    param([Parameter(Mandatory = $true)][int]$Generation)
    $state = [hashtable]::Synchronized(@{
        Generation = $Generation
        IncludeMembers = ($script:uiFlowState -in @("Hosting", "JoinedMember"))
        Phase = "StatusRunning"
        Cancelled = $false
        Process = $null
    })
    $script:asyncPollingAuditCounters.WorkerStarts++
    $script:asyncPollingAuditCounters.ActiveWorkers++
    if ($script:asyncPollingAuditCounters.ActiveWorkers -gt $script:asyncPollingAuditCounters.MaxConcurrentWorkers) {
        $script:asyncPollingAuditCounters.MaxConcurrentWorkers = $script:asyncPollingAuditCounters.ActiveWorkers
    }
    try {
        Start-AutomaticPollingCommand -State $state -Phase "Status"
        return $state
    } catch {
        $script:asyncPollingAuditCounters.ActiveWorkers--
        throw
    }
}

function Process-AutomaticPollingOperation {
    $state = $script:automaticPollingOperation
    if ($null -eq $state) { return }
    if ($script:asyncPollingAudit) {
        $script:asyncPollingAuditCounters.PollingTicks++
        $script:asyncPollingAuditCounters.LastOperationState = [string]$state["Phase"]
    }
    if ([bool]$state["Cancelled"] -or [int]$state["Generation"] -ne $script:automaticPollingGeneration) {
        Stop-StatusRefresh
        return
    }
    $phase = [string]$state["Phase"]
    if ($phase -eq "StatusRunning") {
        if (!(Complete-AutomaticPollingCommand -State $state -Phase "Status")) { return }
        $phase = "StatusDone"
    } elseif ($phase -eq "MembersRunning") {
        if (!(Complete-AutomaticPollingCommand -State $state -Phase "Members")) { return }
        $phase = "MembersDone"
    }
    if ($phase -eq "StatusDone") {
        if ([bool]$state["IncludeMembers"]) {
            $state["Phase"] = "MembersRunning"
            try { Start-AutomaticPollingCommand -State $state -Phase "Members" } catch { $state["Phase"] = "MembersDone"; $state["MembersResult"] = [pscustomobject]@{ ExitCode = 1; Stdout = ""; Stderr = "" } }
            return
        }
        $state["Phase"] = "Ready"
        $phase = "Ready"
    }
    if ($phase -eq "MembersDone") { $state["Phase"] = "Ready"; $phase = "Ready" }
    if ($phase -ne "Ready") { return }
    $result = [pscustomobject]@{
        Status = $state["StatusResult"]
        Members = if ([bool]$state["IncludeMembers"]) { $state["MembersResult"] } else { $null }
    }
    $generation = [int]$state["Generation"]
    $script:automaticPollingOperation = $null
    $script:asyncPollingAuditCounters.ActiveWorkers = [Math]::Max(0, $script:asyncPollingAuditCounters.ActiveWorkers - 1)
    if ($script:cleanupStarted -or $generation -ne $script:automaticPollingGeneration) {
        $script:statusRefreshInProgress = $false
        $script:memberRefreshInProgress = $false
        return
    }
    $script:automaticPollingPendingResult = $result
    $script:automaticPollingPendingGeneration = $generation
    $script:automaticPollingApplyPending = $true
    $queued = Invoke-UiBeginInvoke {
        if ($script:cleanupStarted -or $script:automaticPollingPendingGeneration -ne $script:automaticPollingGeneration) {
            $script:automaticPollingApplyPending = $false
            $script:automaticPollingPendingResult = $null
            $script:statusRefreshInProgress = $false
            $script:memberRefreshInProgress = $false
            return
        }
        try {
            $script:asyncPollingAuditUiThreadId = [System.Threading.Thread]::CurrentThread.ManagedThreadId
            $resultToApply = $script:automaticPollingPendingResult
            $null = Apply-NodeStatusResult $resultToApply.Status -Automatic
            if ($null -ne $resultToApply.Members) { $null = Apply-RoomMembersResult $resultToApply.Members -Automatic }
            if ($script:asyncPollingAudit) {
                $script:asyncPollingAuditCounters.SlowResultApplied = $true
                $script:asyncPollingAuditCounters.UiThreadApplied = ($script:asyncPollingAuditUiThreadId -eq $script:asyncPollingAuditOwnerThreadId)
                if ($null -ne $script:memberMemberGrid) { $script:asyncPollingAuditCounters.MembersRetainedOnFail = ($script:memberMemberGrid.Rows.Count -eq $script:asyncPollingAuditInitialMemberRows) }
            }
        } finally {
            $script:automaticPollingPendingResult = $null
            $script:automaticPollingApplyPending = $false
            $script:statusRefreshInProgress = $false
            $script:memberRefreshInProgress = $false
        }
    }
    if (!$queued) {
        $script:automaticPollingPendingResult = $null
        $script:automaticPollingApplyPending = $false
        $script:statusRefreshInProgress = $false
        $script:memberRefreshInProgress = $false
    }
}

function Invoke-AutomaticStatusRefresh {
    if ($script:primaryBusy -or $script:cleanupStarted) { return }
    if ($script:activePage -eq "Welcome") { return }
    if ($null -ne $script:automaticPollingOperation) {
        Process-AutomaticPollingOperation
        return
    }
    if ($script:statusRefreshInProgress -or $script:automaticPollingApplyPending) { return }
    try {
        $script:statusRefreshInProgress = $true
        $script:memberRefreshInProgress = $script:uiFlowState -in @("Hosting", "JoinedMember")
        $script:automaticPollingOperation = Start-AutomaticPollingOperation -Generation $script:automaticPollingGeneration
    } catch {
        $script:statusRefreshInProgress = $false
        $script:memberRefreshInProgress = $false
        Add-UiLog "自动读取节点状态失败。" "警告"
    }
}

function Stop-StatusRefresh {
    if ($null -ne $script:statusRefreshTimer) {
        $script:statusRefreshTimer.Stop()
    }
    $script:automaticPollingGeneration++
    $script:automaticPollingApplyPending = $false
    $script:automaticPollingPendingResult = $null
    if ($null -ne $script:automaticPollingOperation) {
        $state = $script:automaticPollingOperation
        $state["Cancelled"] = $true
        $process = $state["Process"]
        if ($null -ne $process) {
            try {
                if (!$process.HasExited) { $process.Kill() }
            } catch {}
            try { $process.Dispose() } catch {}
        }
        $script:automaticPollingOperation = $null
        $script:asyncPollingAuditCounters.ActiveWorkers = 0
    }
    $script:statusRefreshInProgress = $false
    $script:memberRefreshInProgress = $false
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
    if ($null -ne $script:hostPageShell) {
        $script:hostPageShell.Visible = ($Name -eq "Host")
        if (!$script:hostPageShell.Visible) { $script:hostPageShell.SetBounds(0, 0, 0, 0) }
    }
    if ($null -ne $script:memberPageShell) {
        $script:memberPageShell.Visible = ($Name -eq "Member")
        if (!$script:memberPageShell.Visible) { $script:memberPageShell.SetBounds(0, 0, 0, 0) }
    }
    if ($null -ne $script:operationSplit) { $script:operationSplit.Panel1.PerformLayout() }
}

function Get-PreferredControlWidth {
    param([Parameter(Mandatory = $true)][System.Windows.Forms.Control]$Control)
    $preferred = $Control.GetPreferredSize((New-Object System.Drawing.Size(0, 0)))
    $width = [Math]::Max([int]$Control.MinimumSize.Width, [int]$preferred.Width)
    if ($Control -is [System.Windows.Forms.DataGridView]) {
        $columnsWidth = 0
        foreach ($column in $Control.Columns) {
            $columnsWidth += $column.GetPreferredWidth([System.Windows.Forms.DataGridViewAutoSizeColumnMode]::AllCells, $true)
        }
        $width = [Math]::Max($width, $columnsWidth)
    } elseif ($Control.Controls.Count -gt 0) {
        if ($Control -is [System.Windows.Forms.TableLayoutPanel]) {
            $columnWidths = @()
            for ($columnIndex = 0; $columnIndex -lt $Control.ColumnCount; $columnIndex++) {
                $styleWidth = 0
                if ($columnIndex -lt $Control.ColumnStyles.Count -and $Control.ColumnStyles[$columnIndex].SizeType -eq [System.Windows.Forms.SizeType]::Absolute) {
                    $styleWidth = [int]$Control.ColumnStyles[$columnIndex].Width
                }
                $columnWidths += $styleWidth
            }
            foreach ($child in $Control.Controls) {
                $position = $Control.GetPositionFromControl($child)
                if ($Control.GetColumnSpan($child) -eq 1 -and $position.Column -lt $columnWidths.Count) {
                    $columnWidths[$position.Column] = [Math]::Max($columnWidths[$position.Column], (Get-PreferredControlWidth $child) + $child.Margin.Horizontal)
                }
            }
            $width = [Math]::Max($width, $Control.Padding.Horizontal + (($columnWidths | Measure-Object -Sum).Sum))
        } else {
            foreach ($child in $Control.Controls) {
                $width = [Math]::Max($width, (Get-PreferredControlWidth $child) + $child.Margin.Horizontal + $Control.Padding.Horizontal)
            }
        }
    }
    return $width
}

function Get-LayoutWidthMeasurement {
    param([Parameter(Mandatory = $true)][System.Windows.Forms.Control]$Control)
    $minimum = $Control.MinimumSize
    $margin = $Control.Margin
    $minimumContent = [Math]::Max(0, [int]$minimum.Width)
    $preferredContent = [Math]::Max($minimumContent, (Get-PreferredControlWidth $Control))
    return [pscustomobject]@{
        MinimumContentWidth = $minimumContent
        PreferredContentWidth = $preferredContent
        MinimumWidth = $minimumContent + $margin.Left + $margin.Right
        PreferredWidth = $preferredContent + $margin.Left + $margin.Right
        MarginLeft = $margin.Left
        MarginRight = $margin.Right
    }
}

function Get-MemberLayoutPolicy {
    param(
        [Parameter(Mandatory = $true)][System.Windows.Forms.TableLayoutPanel]$Shell,
        [Parameter(Mandatory = $true)][System.Windows.Forms.Control]$Settings,
        [Parameter(Mandatory = $true)][System.Windows.Forms.Control]$Members
    )
    $availableWidth = [Math]::Max(0, [int]$Shell.ClientSize.Width - $Shell.Padding.Horizontal)
    $settingsMeasure = Get-LayoutWidthMeasurement $Settings
    $membersMeasure = Get-LayoutWidthMeasurement $Members
    $gap = [Math]::Max(0, [int]$settingsMeasure.MarginRight + [int]$membersMeasure.MarginLeft)
    $wideThreshold = $settingsMeasure.PreferredWidth + $membersMeasure.MinimumWidth
    $mode = if ($availableWidth -ge $wideThreshold) { "Wide" } else { "Narrow" }
    $memberMinimumWidth = $membersMeasure.MinimumWidth
    $memberMaximumWidth = [Math]::Max($memberMinimumWidth, $availableWidth - $settingsMeasure.MinimumWidth)
    $memberWidth = 0
    $settingsWidth = 0
    if ($mode -eq "Wide") {
        $preferredTotal = [Math]::Max(1, $settingsMeasure.PreferredWidth + $membersMeasure.PreferredWidth)
        $extraWidth = [Math]::Max(0, $availableWidth - $preferredTotal)
        $memberShare = [double]$membersMeasure.PreferredWidth / $preferredTotal
        $memberWidth = $membersMeasure.PreferredWidth + [Math]::Round($extraWidth * $memberShare)
        $memberWidth = [Math]::Min($memberMaximumWidth, [Math]::Max($memberMinimumWidth, $memberWidth))
        $settingsWidth = [Math]::Max($settingsMeasure.MinimumWidth, $availableWidth - $memberWidth)
    }
    return [pscustomobject]@{
        AvailableWidth = $availableWidth
        Mode = $mode
        Gap = $gap
        SettingsMinimumWidth = $settingsMeasure.MinimumWidth
        SettingsPreferredWidth = $settingsMeasure.PreferredWidth
        MembersMinimumWidth = $membersMeasure.MinimumWidth
        MembersPreferredWidth = $membersMeasure.PreferredWidth
        MemberMinimumWidth = $memberMinimumWidth
        MemberMaximumWidth = $memberMaximumWidth
        MemberWidth = $memberWidth
        SettingsWidth = $settingsWidth
    }
}

function Set-ResponsiveMemberLayout {
    if ($script:updatingMemberLayout) { return }
    $shell = if ($script:activePage -eq "Host") { $script:hostPageShell } elseif ($script:activePage -eq "Member") { $script:memberPageShell } else { $null }
    $settings = if ($script:activePage -eq "Host") { $script:hostPanel } elseif ($script:activePage -eq "Member") { $script:memberPanel } else { $null }
    $members = if ($script:activePage -eq "Host") { $script:hostMemberPanel } elseif ($script:activePage -eq "Member") { $script:memberMemberPanel } else { $null }
    if ($null -eq $shell -or $null -eq $settings -or $null -eq $members -or $shell.IsDisposed) { return }
    $policy = Get-MemberLayoutPolicy -Shell $shell -Settings $settings -Members $members
    $mode = $policy.Mode
    $script:updatingMemberLayout = $true
    try {
        $shell.SuspendLayout()
        try {
            $shell.ColumnStyles.Clear()
            $shell.RowStyles.Clear()
            if ($mode -eq "Wide") {
                $shell.ColumnCount = 2
                $shell.RowCount = 1
                [void]$shell.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Absolute, $policy.SettingsWidth)))
                [void]$shell.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Absolute, $policy.MemberWidth)))
                [void]$shell.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
                $shell.SetColumn($settings, 0)
                $shell.SetRow($settings, 0)
                $shell.SetColumn($members, 1)
                $shell.SetRow($members, 0)
            } else {
                $shell.ColumnCount = 1
                $shell.RowCount = 2
                [void]$shell.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))
                [void]$shell.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
                [void]$shell.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
                $shell.SetColumn($settings, 0)
                $shell.SetRow($settings, 0)
                $shell.SetColumn($members, 0)
                $shell.SetRow($members, 1)
            }
            $shell.Tag = $mode
            $shell.AccessibleDescription = ($policy | ConvertTo-Json -Compress)
            $shell.PerformLayout()
        } finally {
            $shell.ResumeLayout($true)
        }
    } finally {
        $script:updatingMemberLayout = $false
    }
}

function Set-ResponsiveSplitLayout {
    if ($script:updatingSplitLayout -or $null -eq $script:operationSplit -or $script:operationSplit.IsDisposed) { return }
    if ($script:activePage -eq "Welcome") { return }
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
            Set-ResponsiveMemberLayout
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
        Set-ResponsiveMemberLayout
        Start-StatusRefresh
    }
}

function Show-WelcomePage {
    if ($script:uiFlowState -in @("Hosting", "JoinedMember", "Cleaning")) { return $false }
    if ($script:uiFlowState -in @("HostSetup", "MemberSetup")) {
        if (!(Set-UiFlowState "Idle")) { return $false }
    }
    Show-Page "Welcome"
    Set-UiStatus "等待选择" ([System.Drawing.Color]::MidnightBlue)
    return $true
}

function Show-HostPage {
    if ($script:uiFlowState -ne "Idle") { Add-UiLog "当前房间模式未结束，不能切换到创建网络。" "警告"; return $false }
    if (!(Set-UiFlowState "HostSetup")) { return $false }
    Show-Page "Host"
    $null = Refresh-LocalIPv6
    return $true
}

function Show-MemberPage {
    if ($script:uiFlowState -ne "Idle") { Add-UiLog "当前房间模式未结束，不能切换到加入网络。" "警告"; return $false }
    if (!(Set-UiFlowState "MemberSetup")) { return $false }
    Show-Page "Member"
    Set-UiStatus "请输入房主 IPv6" ([System.Drawing.Color]::MidnightBlue)
    return $true
}

function Return-ToWelcome {
    if ($script:uiFlowState -notin @("HostSetup", "MemberSetup")) { return $false }
    return Show-WelcomePage
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

function New-RoomMembersPanel {
    param([Parameter(Mandatory = $true)][string]$Name)
    $panel = New-Object System.Windows.Forms.GroupBox
    $panel.Name = $Name
    $panel.Text = "房间成员（0）"
    $panel.Dock = [System.Windows.Forms.DockStyle]::Fill
    $panel.MinimumSize = New-Object System.Drawing.Size(0, 0)
    $panel.Padding = New-Object System.Windows.Forms.Padding(10, 8, 10, 10)

    $layout = New-Object System.Windows.Forms.TableLayoutPanel
    $layout.Name = "$Name`Layout"
    $layout.Dock = [System.Windows.Forms.DockStyle]::Fill
    $layout.ColumnCount = 2
    $layout.RowCount = 2
    [void]$layout.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))
    [void]$layout.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::AutoSize)))
    [void]$layout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
    [void]$layout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 100)))
    [void]$panel.Controls.Add($layout)

    $countLabel = New-LayoutLabel "$Name`Count" "房间成员（0）"
    $countLabel.Margin = New-Object System.Windows.Forms.Padding(2, 2, 6, 6)
    [void]$layout.Controls.Add($countLabel, 0, 0)
    $refreshLabel = New-LayoutLabel "$Name`Refresh" "尚未加入房间"
    $refreshLabel.AutoEllipsis = $true
    $refreshLabel.Margin = New-Object System.Windows.Forms.Padding(6, 2, 2, 6)
    $refreshLabel.Anchor = [System.Windows.Forms.AnchorStyles]::Right
    [void]$layout.Controls.Add($refreshLabel, 1, 0)

    $grid = New-Object System.Windows.Forms.DataGridView
    $grid.Name = "$Name`Grid"
    $grid.Dock = [System.Windows.Forms.DockStyle]::Fill
    $grid.MinimumSize = New-Object System.Drawing.Size(0, 0)
    $grid.ReadOnly = $true
    $grid.AllowUserToAddRows = $false
    $grid.AllowUserToDeleteRows = $false
    $grid.AllowUserToOrderColumns = $false
    $grid.AllowUserToResizeRows = $false
    $grid.AutoGenerateColumns = $false
    $grid.AutoSizeColumnsMode = [System.Windows.Forms.DataGridViewAutoSizeColumnsMode]::Fill
    $grid.ColumnHeadersHeightSizeMode = [System.Windows.Forms.DataGridViewColumnHeadersHeightSizeMode]::AutoSize
    $grid.RowHeadersVisible = $false
    $grid.MultiSelect = $false
    $grid.SelectionMode = [System.Windows.Forms.DataGridViewSelectionMode]::CellSelect
    foreach ($header in @("名称", "虚拟 IPv4", "状态")) {
        $column = New-Object System.Windows.Forms.DataGridViewTextBoxColumn
        $column.HeaderText = $header
        $column.Name = $header
        $column.ReadOnly = $true
        $column.SortMode = [System.Windows.Forms.DataGridViewColumnSortMode]::NotSortable
        [void]$grid.Columns.Add($column)
    }
    $grid.AutoSizeColumnsMode = [System.Windows.Forms.DataGridViewAutoSizeColumnsMode]::AllCells
    $grid.PerformLayout()
    $minimumGridWidth = 0
    foreach ($column in $grid.Columns) {
        $minimumGridWidth += $column.GetPreferredWidth([System.Windows.Forms.DataGridViewAutoSizeColumnMode]::AllCells, $true)
    }
    if ($minimumGridWidth -gt 0) {
        $minimumGridHeight = [Math]::Max(0, [int]$grid.ColumnHeadersHeight)
        $grid.MinimumSize = New-Object System.Drawing.Size($minimumGridWidth, $minimumGridHeight)
        $panel.MinimumSize = New-Object System.Drawing.Size(($minimumGridWidth + $panel.Padding.Horizontal), ($minimumGridHeight + $panel.Padding.Vertical))
    }
    $grid.AutoSizeColumnsMode = [System.Windows.Forms.DataGridViewAutoSizeColumnsMode]::Fill
    [void]$layout.Controls.Add($grid, 0, 1)
    $layout.SetColumnSpan($grid, 2)
    return [pscustomobject]@{ Panel = $panel; Grid = $grid; CountLabel = $countLabel; RefreshLabel = $refreshLabel }
}

function Set-RoomMemberRows {
    param([Parameter(Mandatory = $true)][object[]]$Members)
    $rows = @($Members)
    foreach ($grid in @($script:hostMemberGrid, $script:memberMemberGrid)) {
        if ($null -eq $grid -or $grid.IsDisposed) { continue }
        $grid.Rows.Clear()
        foreach ($member in $rows) {
            $displayName = [string]$member.display_name
            if ([bool]$member.is_local) { $displayName += "（本机）" }
            $state = if ([string]$member.state -eq "online") { "在线" } else { "在线" }
            [void]$grid.Rows.Add($displayName, [string]$member.virtual_ipv4, $state)
        }
        $grid.ClearSelection()
    }
    foreach ($label in @($script:hostMemberCountLabel, $script:memberMemberCountLabel)) {
        if ($null -ne $label -and !$label.IsDisposed) { $label.Text = "房间成员（$($rows.Count)）" }
    }
    foreach ($panel in @($script:hostMemberPanel, $script:memberMemberPanel)) {
        if ($null -ne $panel -and !$panel.IsDisposed) { $panel.Text = "房间成员（$($rows.Count)）" }
    }
    foreach ($label in @($script:hostMemberRefreshLabel, $script:memberMemberRefreshLabel)) {
        if ($null -ne $label -and !$label.IsDisposed) { $label.Text = "已更新" }
    }
}

function Clear-RoomMembers {
    foreach ($grid in @($script:hostMemberGrid, $script:memberMemberGrid)) {
        if ($null -ne $grid -and !$grid.IsDisposed) { $grid.Rows.Clear() }
    }
    foreach ($label in @($script:hostMemberCountLabel, $script:memberMemberCountLabel)) {
        if ($null -ne $label -and !$label.IsDisposed) { $label.Text = "房间成员（0）" }
    }
    foreach ($panel in @($script:hostMemberPanel, $script:memberMemberPanel)) {
        if ($null -ne $panel -and !$panel.IsDisposed) { $panel.Text = "房间成员（0）" }
    }
    foreach ($label in @($script:hostMemberRefreshLabel, $script:memberMemberRefreshLabel)) {
        if ($null -ne $label -and !$label.IsDisposed) { $label.Text = "尚未加入房间" }
    }
    $script:hasMemberRefreshResult = $false
    $script:lastMemberRefreshSucceeded = $false
    $script:lastMemberFingerprint = ""
}

function New-RoomPageShell {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][System.Windows.Forms.Control]$Settings,
        [Parameter(Mandatory = $true)][System.Windows.Forms.Control]$Members
    )
    $shell = New-Object System.Windows.Forms.TableLayoutPanel
    $shell.Name = $Name
    $shell.Dock = [System.Windows.Forms.DockStyle]::Top
    $shell.AutoSize = $true
    $shell.AutoSizeMode = [System.Windows.Forms.AutoSizeMode]::GrowAndShrink
    $shell.ColumnCount = 1
    $shell.RowCount = 2
    [void]$shell.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))
    [void]$shell.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
    [void]$shell.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
    [void]$shell.Controls.Add($Settings, 0, 0)
    [void]$shell.Controls.Add($Members, 0, 1)
    return $shell
}

function New-ResponsivePageGrid {
    param([string]$Name)
    $page = New-Object System.Windows.Forms.TableLayoutPanel
    $page.Name = $Name
    $page.Dock = [System.Windows.Forms.DockStyle]::Top
    $page.AutoSize = $true
    $page.AutoSizeMode = [System.Windows.Forms.AutoSizeMode]::GrowAndShrink
    $page.MinimumSize = New-Object System.Drawing.Size(0, 0)
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
        @{ Name = "large-font"; Width = 900; Height = 640; Font = 16; Distance = -1 },
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
            $splitterBeforeMemberLayout = $script:operationSplit.SplitterDistance
            Set-ResponsiveMemberLayout
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

            $hostPageShellVisible = $null -ne $script:hostPageShell -and $script:hostPageShell.Visible
            $memberPageShellVisible = $null -ne $script:memberPageShell -and $script:memberPageShell.Visible
            $hostPageShellArea = if ($null -ne $script:hostPageShell) { [int]$script:hostPageShell.Bounds.Width * [int]$script:hostPageShell.Bounds.Height } else { 0 }
            $memberPageShellArea = if ($null -ne $script:memberPageShell) { [int]$script:memberPageShell.Bounds.Width * [int]$script:memberPageShell.Bounds.Height } else { 0 }
            if ($hostPageShellVisible -ne ($page -eq "Host") -or $memberPageShellVisible -ne ($page -eq "Member")) {
                [void]$errors.Add("$($case.Name)/$page page-shell visibility mismatch")
            }
            if ((!$hostPageShellVisible -and $hostPageShellArea -ne 0) -or (!$memberPageShellVisible -and $memberPageShellArea -ne 0)) {
                [void]$errors.Add("$($case.Name)/$page inactive page shell occupies space")
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
            $memberMode = "Narrow"
            $memberPanelWidth = 0
            $memberPanelHeight = 0
            $memberGridWidth = 0
            $memberGridHeight = 0
            $settingsMemberOverlap = 0
            $memberMinimumWidth = 0
            $memberMaximumWidth = 0
            $memberWidthWithinBounds = $true
            if ($wantDiagnostics) {
                $memberShell = if ($page -eq "Host") { $script:hostPageShell } else { $script:memberPageShell }
                $memberPanel = if ($page -eq "Host") { $script:hostMemberPanel } else { $script:memberMemberPanel }
                $memberGrid = if ($page -eq "Host") { $script:hostMemberGrid } else { $script:memberMemberGrid }
                $settingsPanel = if ($page -eq "Host") { $script:hostPanel } else { $script:memberPanel }
                $policy = Get-MemberLayoutPolicy -Shell $memberShell -Settings $settingsPanel -Members $memberPanel
                $memberMode = if ($null -ne $memberShell.Tag) { [string]$memberShell.Tag } else { "Narrow" }
                $memberMinimumWidth = [int]$policy.MemberMinimumWidth
                $memberMaximumWidth = [int]$policy.MemberMaximumWidth
                $settingsRectangle = $settingsPanel.RectangleToScreen($settingsPanel.ClientRectangle)
                $memberRectangle = $memberPanel.RectangleToScreen($memberPanel.ClientRectangle)
                $memberGridRectangle = $memberGrid.RectangleToScreen($memberGrid.ClientRectangle)
                $settingsMemberIntersection = [System.Drawing.Rectangle]::Intersect($settingsRectangle, $memberRectangle)
                $settingsMemberOverlap = if ($settingsMemberIntersection.Width -gt 0 -and $settingsMemberIntersection.Height -gt 0) { 1 } else { 0 }
                $memberPanelWidth = $memberRectangle.Width
                $memberPanelHeight = $memberRectangle.Height
                $memberGridWidth = $memberGridRectangle.Width
                $memberGridHeight = $memberGridRectangle.Height
                $actualMemberWidth = [int]$memberPanel.Bounds.Width + $memberPanel.Margin.Left + $memberPanel.Margin.Right
                $memberWidthWithinBounds = $memberMode -ne "Wide" -or ($actualMemberWidth -ge $memberMinimumWidth -and $actualMemberWidth -le $memberMaximumWidth)
                $expectedMemberMode = [string]$policy.Mode
                if ($memberMode -ne $expectedMemberMode) { [void]$errors.Add("$($case.Name)/$page member layout mode mismatch: $memberMode") }
                if ($memberPanelWidth -le 0 -or $memberPanelHeight -le 0 -or $memberGridWidth -le 0 -or $memberGridHeight -le 0) { [void]$errors.Add("$($case.Name)/$page member panel or grid has no usable area") }
                if ($settingsMemberOverlap -ne 0) { [void]$errors.Add("$($case.Name)/$page settings and member panel overlap") }
                if (!$memberWidthWithinBounds) { [void]$errors.Add("$($case.Name)/$page member width is outside measured bounds") }
            }
            [void]$samples.Add([pscustomobject]@{
                Case = $case.Name
                Page = $page
                InputWidth = $inputWidth
                LogWidth = if ($wantDiagnostics) { $script:logBox.ClientSize.Width } else { 0 }
                LogHeight = if ($wantDiagnostics) { $script:logBox.ClientSize.Height } else { 0 }
                SplitterDistance = if ($wantDiagnostics) { $script:operationSplit.SplitterDistance } else { 0 }
                MemberLayoutMode = $memberMode
                MemberPanelWidth = $memberPanelWidth
                MemberPanelHeight = $memberPanelHeight
                MemberGridWidth = $memberGridWidth
                MemberGridHeight = $memberGridHeight
                SettingsMemberOverlap = $settingsMemberOverlap
                HostPageShellVisible = $hostPageShellVisible
                MemberPageShellVisible = $memberPageShellVisible
                HostPageShellArea = $hostPageShellArea
                MemberPageShellArea = $memberPageShellArea
                MemberMinimumWidth = $memberMinimumWidth
                MemberMaximumWidth = $memberMaximumWidth
                MemberWidthWithinBounds = $memberWidthWithinBounds
                SplitterPreserved = ($splitterBeforeMemberLayout -eq $script:operationSplit.SplitterDistance)
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

    foreach ($page in @("Host", "Member")) {
        $preferred = $samples | Where-Object { $_.Case -eq "preferred" -and $_.Page -eq $page } | Select-Object -First 1
        $large = $samples | Where-Object { $_.Case -eq "large" -and $_.Page -eq $page } | Select-Object -First 1
        if ($preferred.MemberLayoutMode -eq "Wide" -and $large.MemberLayoutMode -eq "Wide" -and $large.MemberPanelWidth -le $preferred.MemberPanelWidth) {
            [void]$errors.Add("$page member width did not receive a share of extra wide-screen space")
        }
    }

    return [pscustomobject]@{
        Passed = $errors.Count -eq 0
        Errors = $errors.ToArray()
        Samples = $samples.ToArray()
    }
}

function Invoke-AsyncPollingAudit {
    $script:asyncPollingAuditOwnerThreadId = [System.Threading.Thread]::CurrentThread.ManagedThreadId
    $script:form.ShowInTaskbar = $false
    $script:form.StartPosition = [System.Windows.Forms.FormStartPosition]::Manual
    $script:form.Location = New-Object System.Drawing.Point(-32000, -32000)
    $script:form.Opacity = 0
    $script:form.Show()
    $script:uiFlowState = "Hosting"
    $script:activeNetworkId = "audit-network"
    Set-PageLayoutState "Host"
    Set-ResponsiveSplitLayout
    Set-ResponsiveMemberLayout
    $initialMembers = @([pscustomobject]@{
        display_name = "audit-host"
        virtual_ipv4 = "10.42.0.1"
        is_local = $true
        state = "online"
    })
    Set-RoomMemberRows $initialMembers
    $script:asyncPollingAuditInitialMemberRows = $script:memberMemberGrid.Rows.Count

    $heartbeat = New-Object System.Windows.Forms.Timer
    $heartbeat.Interval = 50
    $script:asyncPollingAuditStage = "responsive"
    $script:asyncPollingAuditResponsiveDeadline = (Get-Date).AddMilliseconds(1200)
    $script:asyncPollingAuditCompletionDeadline = (Get-Date).AddSeconds(10)
    $heartbeat.Add_Tick({
        try {
            $script:asyncPollingAuditCounters.MessagePumpTicks++
            $now = Get-Date
            if ($script:asyncPollingAuditStage -eq "responsive" -and $now -ge $script:asyncPollingAuditResponsiveDeadline) {
                $script:asyncPollingAuditResponsiveTicks = $script:asyncPollingAuditCounters.MessagePumpTicks
                $script:asyncPollingAuditStage = "waiting"
            }
            if ($script:asyncPollingAuditStage -eq "waiting" -and $script:asyncPollingAuditCounters.SlowResultApplied) {
                Stop-StatusRefresh
                $script:uiFlowState = "Idle"
                $script:asyncPollingAuditLateQueued = Invoke-UiBeginInvoke {
                    if ($null -ne $script:nodeStatusLabel -and !$script:nodeStatusLabel.IsDisposed) {
                        $script:asyncPollingAuditCounters.LateResultWrites++
                        $script:nodeStatusLabel.Text = "迟到结果不应写入"
                    }
                }
                $script:asyncPollingAuditStage = "closing"
                $script:form.Close()
            } elseif ($script:asyncPollingAuditStage -eq "waiting" -and $now -ge $script:asyncPollingAuditCompletionDeadline) {
                $script:asyncPollingAuditStage = "closing"
                $script:form.Close()
            }
        } catch {
            $script:asyncPollingAuditStage = "closing"
            try { $script:form.Close() } catch {}
        }
    })
    $heartbeat.Start()
    $script:statusRefreshTimer.Interval = 50
    Start-StatusRefresh
    [void][System.Windows.Forms.Application]::Run($script:form)
    $heartbeat.Stop()
    $heartbeat.Dispose()

    return [pscustomobject]@{
        Passed = ($script:asyncPollingAuditResponsiveTicks -ge 10 -and
            $script:asyncPollingAuditCounters.WorkerStarts -eq 1 -and
            $script:asyncPollingAuditCounters.MaxConcurrentWorkers -le 1 -and
            $script:asyncPollingAuditCounters.LateResultWrites -eq 0 -and
            $script:asyncPollingAuditCounters.UiThreadApplied -and
            $script:asyncPollingAuditCounters.SlowResultApplied -and
            $script:asyncPollingAuditCounters.MembersRetainedOnFail -and
            $script:asyncPollingAuditLateQueued)
        MessagePumpTicks = $script:asyncPollingAuditResponsiveTicks
        WorkerStarts = $script:asyncPollingAuditCounters.WorkerStarts
        MaxConcurrentWorkers = $script:asyncPollingAuditCounters.MaxConcurrentWorkers
        LateResultWrites = $script:asyncPollingAuditCounters.LateResultWrites
        UiThreadApplied = $script:asyncPollingAuditCounters.UiThreadApplied
        SlowResultApplied = $script:asyncPollingAuditCounters.SlowResultApplied
        MembersRetainedOnFail = $script:asyncPollingAuditCounters.MembersRetainedOnFail
        PollingTicks = $script:asyncPollingAuditCounters.PollingTicks
        LastOperationState = $script:asyncPollingAuditCounters.LastOperationState
        LateResultQueued = $script:asyncPollingAuditLateQueued
    }
}

if (!$LayoutAudit) {
    if (!(Enter-UiInstance)) {
        [void][System.Windows.Forms.MessageBox]::Show("IPv6Mesh 已在运行。请使用现有窗口。", "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Information)
        return
    }
    $script:uiInstanceActive = $true
}

try {
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
$script:welcomeCreateButton = $createButton
[void]$script:welcomePanel.Controls.Add($createButton, 1, 3)

$joinButton = New-Object System.Windows.Forms.Button
$joinButton.Name = "WelcomeJoin"
$joinButton.Text = "加入网络"
$joinButton.Dock = [System.Windows.Forms.DockStyle]::Fill
$joinButton.Margin = New-Object System.Windows.Forms.Padding(10, 15, 10, 15)
$joinButton.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 12)
$joinButton.Add_Click({ Show-MemberPage })
$script:welcomeJoinButton = $joinButton
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
$script:operationSplit.Panel1.Add_SizeChanged({ Set-ResponsiveMemberLayout })
$script:form.Add_Shown({ Set-ResponsiveSplitLayout })

$script:hostPanel = New-ResponsivePageGrid "HostPanel"
$script:hostPanel.RowCount = 6
for ($row = 0; $row -lt 6; $row++) {
    [void]$script:hostPanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
}

$hostBackButton = New-LayoutButton "HostBack" "返回" 90
$hostBackButton.Add_Click({ Return-ToWelcome })
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
$hostMemberInfo = New-RoomMembersPanel "HostMembers"
$script:hostMemberPanel = $hostMemberInfo.Panel
$script:hostMemberGrid = $hostMemberInfo.Grid
$script:hostMemberCountLabel = $hostMemberInfo.CountLabel
$script:hostMemberRefreshLabel = $hostMemberInfo.RefreshLabel
$script:hostPageShell = New-RoomPageShell "HostPageShell" $script:hostPanel $script:hostMemberPanel
[void]$script:operationSplit.Panel1.Controls.Add($script:hostPageShell)

$script:memberPanel = New-ResponsivePageGrid "MemberPanel"
$script:memberPanel.RowCount = 6
for ($row = 0; $row -lt 6; $row++) {
    [void]$script:memberPanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
}
$script:memberPanel.Visible = $false

$memberBackButton = New-LayoutButton "MemberBack" "返回" 90
$memberBackButton.Add_Click({ Return-ToWelcome })
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
$memberMemberInfo = New-RoomMembersPanel "MemberMembers"
$script:memberMemberPanel = $memberMemberInfo.Panel
$script:memberMemberGrid = $memberMemberInfo.Grid
$script:memberMemberCountLabel = $memberMemberInfo.CountLabel
$script:memberMemberRefreshLabel = $memberMemberInfo.RefreshLabel
$script:memberPageShell = New-RoomPageShell "MemberPageShell" $script:memberPanel $script:memberMemberPanel
[void]$script:operationSplit.Panel1.Controls.Add($script:memberPageShell)

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
$script:refreshStatusButton = $refreshStatusButton
[void]$statusActions.Controls.Add($refreshStatusButton)
$connectButton = New-LayoutButton "ConnectNode" "连接" 90
$connectButton.Add_Click({ Connect-Node })
$script:connectButton = $connectButton
[void]$statusActions.Controls.Add($connectButton)
$disconnectButton = New-LayoutButton "DisconnectNode" "断开" 90
$disconnectButton.Add_Click({ Disconnect-Node })
$script:disconnectButton = $disconnectButton
[void]$statusActions.Controls.Add($disconnectButton)
$leaveButton = New-LayoutButton "LeaveRoom" "离开房间" 110
$leaveButton.Add_Click({ Exit-ActiveRoom })
$script:leaveButton = $leaveButton
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
    try { Update-ControlEndpoint } catch { Update-UiFlowControls }
})

$script:form.Add_FormClosing({
    Stop-AllResources
})
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
if ($AsyncPollingAudit) {
    try {
        $audit = Invoke-AsyncPollingAudit
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
[void][System.Windows.Forms.Application]::Run($script:form)
} finally {
    if ($script:uiInstanceActive) {
        Stop-AllResources
        Exit-UiInstance
        $script:uiInstanceActive = $false
    }
}
