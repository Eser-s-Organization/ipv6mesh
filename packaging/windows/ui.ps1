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
$script:logBox = $null
$script:statusLabel = $null
$script:controlUrlBox = $null
$script:ipv6AddressBox = $null
$script:portBox = $null
$script:listenAddressBox = $null
$script:adminTokenBox = $null
$script:networkNameBox = $null
$script:poolBox = $null
$script:expiryBox = $null
$script:networkIdBox = $null
$script:hostInviteBox = $null
$script:memberInviteBox = $null
$script:nodeInviteBox = $null
$script:deviceNameBox = $null
$script:roleBox = $null
$script:nodeStatusLabel = $null
$script:roleHintLabel = $null
$script:nodeActionButton = $null
$script:cleanupStarted = $false
$script:updatingEndpoint = $false

function Add-UiLog {
    param([Parameter(Mandatory = $true)][string]$Message, [string]$Level = "信息")
    $line = "[{0}] [{1}] {2}" -f (Get-Date).ToString("HH:mm:ss"), $Level, $Message
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

function Get-DetectedIPv6Address {
    try {
        $addresses = @(Get-NetIPAddress -AddressFamily IPv6 -ErrorAction Stop)
    } catch {
        return ""
    }

    $candidates = foreach ($entry in $addresses) {
        $value = ([string]$entry.IPAddress).Trim()
        if ([string]::IsNullOrWhiteSpace($value)) { continue }
        try {
            $parsed = [System.Net.IPAddress]::Parse($value)
            $bytes = $parsed.GetAddressBytes()
            if ($bytes.Length -ne 16) { continue }
            if ($value -eq "::1") { continue }
            $isLinkLocal = $bytes[0] -eq 0xfe -and (($bytes[1] -band 0xc0) -eq 0x80)
            if ($isLinkLocal) { continue }
            $isGlobal = (($bytes[0] -band 0xe0) -eq 0x20)
            [pscustomobject]@{
                Address = $value
                IsGlobal = $isGlobal
                IsPreferred = ([string]$entry.AddressState -eq "Preferred")
                SkipAsSource = [bool]$entry.SkipAsSource
                PrefixOrigin = [string]$entry.PrefixOrigin
                InterfaceIndex = [int]$entry.InterfaceIndex
            }
        } catch {
            continue
        }
    }

    $global = @($candidates | Where-Object { $_.IsGlobal } | Sort-Object `
        @{ Expression = { if ($_.IsPreferred) { 0 } else { 1 } } }, `
        @{ Expression = { if ($_.SkipAsSource) { 1 } else { 0 } } }, `
        @{ Expression = { if ($_.PrefixOrigin -eq "RouterAdvertisement") { 0 } else { 1 } } }, `
        InterfaceIndex)
    if ($global.Count -gt 0) { return [string]$global[0].Address }

    # A ULA is not normally reachable from the public Internet, but it is a
    # useful fallback for a controlled IPv6-only LAN test.
    $fallback = @($candidates | Sort-Object `
        @{ Expression = { if ($_.IsPreferred) { 0 } else { 1 } } }, `
        @{ Expression = { if ($_.SkipAsSource) { 1 } else { 0 } } }, `
        InterfaceIndex)
    if ($fallback.Count -gt 0) { return [string]$fallback[0].Address }
    return ""
}

function Get-ControlPort {
    $value = Get-BoxText $script:portBox
    $port = 0
    if (![int]::TryParse($value, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        throw "控制面端口必须是 1 到 65535 之间的数字。"
    }
    return $port
}

function Update-ControlEndpoint {
    $address = (Get-BoxText $script:ipv6AddressBox).Trim('[', ']')
    if ($address -eq "") { throw "本机 IPv6 不能为空。" }
    try {
        $parsed = [System.Net.IPAddress]::Parse($address)
        $bytes = $parsed.GetAddressBytes()
        if ($bytes.Length -ne 16) { throw "不是 IPv6 地址。" }
        $isLinkLocal = $bytes[0] -eq 0xfe -and (($bytes[1] -band 0xc0) -eq 0x80)
        if ($isLinkLocal) { throw "不能使用 fe80:: 链路本地地址作为远程控制面地址。" }
    } catch {
        throw "本机 IPv6 无效：$($_.Exception.Message)"
    }
    $port = Get-ControlPort
    $script:updatingEndpoint = $true
    try {
        $script:ipv6AddressBox.Text = $address
        $script:controlUrlBox.Text = "http://[$address]:$port"
        $script:listenAddressBox.Text = "[::]:$port"
    } finally {
        $script:updatingEndpoint = $false
    }
    return $script:controlUrlBox.Text
}

function Refresh-LocalIPv6 {
    try {
        $address = Get-DetectedIPv6Address
        if ($address -eq "") { throw "没有检测到可用的非链路本地 IPv6 地址。请确认网卡已启用 IPv6。" }
        $script:ipv6AddressBox.Text = $address
        $url = Update-ControlEndpoint
        Add-UiLog "已自动检测本机 IPv6：$address；已生成控制面 URL：$url"
        Set-UiStatus "已生成控制面 URL" ([System.Drawing.Color]::ForestGreen)
    } catch {
        Add-UiLog "自动检测本机 IPv6 失败：$($_.Exception.Message)" "警告"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("自动检测 IPv6 失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Warning)
    }
}

function Get-PowerShellPath {
    $candidate = Join-Path $env:WINDIR "System32\WindowsPowerShell\v1.0\powershell.exe"
    if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    return "powershell.exe"
}

function Get-PayloadExecutable {
    param([Parameter(Mandatory = $true)][string]$Name)
    $installed = Join-Path $InstallDirectory $Name
    if (Test-Path -LiteralPath $installed -PathType Leaf) { return $installed }
    $packaged = Join-Path $PackageDirectory $Name
    if (Test-Path -LiteralPath $packaged -PathType Leaf) { return $packaged }
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

function Invoke-External {
    param(
        [Parameter(Mandatory = $true)][string]$FileName,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [hashtable]$Environment = @{},
        [Parameter(Mandatory = $true)][string]$Source,
        [switch]$SuppressStandardOutput
    )
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $FileName
    $psi.Arguments = (($Arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join " ")
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($key in $Environment.Keys) { $psi.EnvironmentVariables[$key] = [string]$Environment[$key] }
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $psi
    Add-UiLog "开始执行 $Source" "调试"
    try {
        [void]$process.Start()
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.Result
        $stderr = $stderrTask.Result
        $exitCode = $process.ExitCode
    } catch {
        Add-UiLog "$Source 启动失败：$($_.Exception.Message)" "错误"
        throw
    } finally {
        $process.Dispose()
    }
    if (!$SuppressStandardOutput -and ![string]::IsNullOrWhiteSpace($stdout)) {
        foreach ($line in ($stdout -split [Environment]::NewLine)) {
            if (![string]::IsNullOrWhiteSpace($line)) { Add-UiLog "$Source：$line" }
        }
    }
    if (![string]::IsNullOrWhiteSpace($stderr)) {
        foreach ($line in ($stderr -split [Environment]::NewLine)) {
            if (![string]::IsNullOrWhiteSpace($line)) { Add-UiLog "$Source：$line" "警告" }
        }
    }
    if ($exitCode -ne 0) { Add-UiLog "$Source 退出码：$exitCode" "错误" }
    else { Add-UiLog "$Source 执行完成" "调试" }
    return [pscustomobject]@{ ExitCode = $exitCode; Stdout = $stdout; Stderr = $stderr }
}

function Get-ClientEnvironment {
    $environment = @{}
    $url = Get-BoxText $script:controlUrlBox
    if ($url -ne "") { $environment["IPV6MESH_CONTROL_URL"] = $url }
    $adminToken = Get-BoxText $script:adminTokenBox
    if ($adminToken -ne "") { $environment["IPV6MESH_ADMIN_TOKEN"] = $adminToken }
    return $environment
}

function Assert-ControlUrl {
    $value = Get-BoxText $script:controlUrlBox
    $parsed = $null
    if (![Uri]::TryCreate($value, [UriKind]::Absolute, [ref]$parsed) -or $parsed.Scheme -notin @("http", "https") -or [string]::IsNullOrWhiteSpace($parsed.Host)) {
        throw "控制面 URL 无效。IPv6 地址必须写成 http://[IPv6]:8080。"
    }
    return $value.TrimEnd("/")
}

function Convert-ResultToJson {
    param([Parameter(Mandatory = $true)]$Result, [Parameter(Mandatory = $true)][string]$Operation)
    if ($Result.ExitCode -ne 0) { throw "$Operation 失败，请查看日志窗口中的错误信息。" }
    if ([string]::IsNullOrWhiteSpace($Result.Stdout)) { throw "$Operation 没有返回 JSON 结果。" }
    try { return ($Result.Stdout | ConvertFrom-Json -ErrorAction Stop) }
    catch {
        Add-UiLog "$Operation 返回内容不是有效 JSON：$($_.Exception.Message)" "错误"
        throw
    }
}

function Invoke-VpnCtl {
    param([Parameter(Mandatory = $true)][string[]]$Arguments, [switch]$SuppressStandardOutput)
    $spec = @{
        FileName = (Get-PayloadExecutable "vpnctl.exe")
        Arguments = $Arguments
        Environment = (Get-ClientEnvironment)
        Source = "vpnctl"
        SuppressStandardOutput = $SuppressStandardOutput
    }
    return Invoke-External @spec
}

function Test-ControlHealth {
    try {
        $url = Assert-ControlUrl
        $request = [System.Net.HttpWebRequest]::Create($url + "/healthz")
        $request.Proxy = $null
        $request.Timeout = 15000
        $response = $request.GetResponse()
        try {
            $reader = New-Object System.IO.StreamReader($response.GetResponseStream())
            try { $body = $reader.ReadToEnd().Trim() } finally { $reader.Dispose() }
            Add-UiLog "控制面健康检查：HTTP $([int]$response.StatusCode)，响应：$body"
            Set-UiStatus "控制面可访问" ([System.Drawing.Color]::ForestGreen)
        } finally { $response.Dispose() }
        return $true
    } catch {
        Add-UiLog "控制面健康检查失败：$($_.Exception.Message)" "错误"
        Set-UiStatus "控制面不可访问" ([System.Drawing.Color]::Firebrick)
        return $false
    }
}

function Open-ControlFirewall {
    param([Parameter(Mandatory = $true)][string]$ListenAddress)
    $port = 8080
    if ($ListenAddress -match ':(\d+)$') {
        $port = [int]$Matches[1]
    }
    $ruleName = "IPv6Mesh Control Plane TCP $port"
    try {
        if (!(Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) {
            New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Protocol TCP -LocalPort $port -Action Allow -Profile Any | Out-Null
            Add-UiLog "已放行控制面 TCP $port 防火墙规则。"
        } else {
            Add-UiLog "控制面 TCP $port 防火墙规则已存在。"
        }
    } catch {
        Add-UiLog "自动放行控制面防火墙失败：$($_.Exception.Message)；请手动放行 TCP $port。" "警告"
    }
}

function Start-ControlPlane {
    try {
        $null = Assert-ControlUrl
        $port = Get-ControlPort
        $listenAddress = "[::]:$port"
        $script:listenAddressBox.Text = $listenAddress
        $adminToken = Get-BoxText $script:adminTokenBox
        $expiry = Get-BoxText $script:expiryBox
        if ($listenAddress -eq "") { throw "监听地址不能为空，例如 [::]:8080。" }
        if ($adminToken -eq "") { throw "管理员令牌不能为空，请使用长随机令牌。" }
        if ($script:controlProcess -and !$script:controlProcess.HasExited) {
            Add-UiLog "控制面已经由本窗口启动。"
            return
        }
        Open-ControlFirewall $listenAddress
        $psi = New-Object System.Diagnostics.ProcessStartInfo
        $psi.FileName = Get-PayloadExecutable "control-server.exe"
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $psi.RedirectStandardOutput = $true
        $psi.RedirectStandardError = $true
        $psi.EnvironmentVariables["CONTROL_LISTEN_ADDRESS"] = $listenAddress
        $psi.EnvironmentVariables["CONTROL_BOOTSTRAP_TOKEN"] = $adminToken
        $psi.EnvironmentVariables["CONTROL_SESSION_TTL"] = "24h"
        $psi.EnvironmentVariables["CONTROL_INVITE_TTL"] = if ($expiry -eq "") { "24h" } else { $expiry }
        $psi.EnvironmentVariables["CONTROL_REPOSITORY_MODE"] = "memory"
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
            Add-UiLog "控制面进程已退出，退出码：$($Event.Sender.ExitCode)" "警告"
        })
        [void]$process.Start()
        $process.BeginOutputReadLine()
        $process.BeginErrorReadLine()
        $script:controlProcess = $process
        Add-UiLog "控制面已启动：$($psi.FileName)"
        Add-UiLog "监听地址：$listenAddress；当前使用内存仓库。"
        Set-UiStatus "控制面运行中" ([System.Drawing.Color]::ForestGreen)
    } catch {
        Add-UiLog "启动控制面失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("启动控制面失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
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
        } catch { Add-UiLog "停止控制面进程时出现问题：$($_.Exception.Message)" "警告" }
        finally { $script:controlProcess.Dispose(); $script:controlProcess = $null }
    }
}

function Stop-NodeService {
    try {
        $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($null -eq $service) {
            Add-UiLog "未发现本机 IPv6Mesh 服务。"
            return
        }
        if ($service.Status -eq [System.ServiceProcess.ServiceControllerStatus]::Stopped) {
            Add-UiLog "IPv6Mesh 服务已经停止。"
            return
        }
        Add-UiLog "正在停止 IPv6Mesh 服务并清理 WireGuard 适配器、虚拟 IPv4 地址和路由……"
        Stop-Service -Name $ServiceName -Force -ErrorAction Stop
        $deadline = (Get-Date).AddSeconds(20)
        do {
            $service.Refresh()
            if ($service.Status -eq [System.ServiceProcess.ServiceControllerStatus]::Stopped) { break }
            if ((Get-Date) -ge $deadline) { throw "等待 IPv6Mesh 服务停止超时。" }
            Start-Sleep -Milliseconds 250
        } while ($true)
        Add-UiLog "IPv6Mesh 服务已停止，本机网络资源已释放。"
    } catch {
        Add-UiLog "停止 IPv6Mesh 服务失败：$($_.Exception.Message)" "警告"
    }
}

function Stop-AllResources {
    if ($script:cleanupStarted) { return }
    $script:cleanupStarted = $true
    Add-UiLog "正在执行退出清理……"
    Stop-NodeService
    Stop-ControlPlane
    Add-UiLog "退出清理完成。"
}

function Create-Network {
    try {
        $null = Assert-ControlUrl
        if ((Get-BoxText $script:adminTokenBox) -eq "") { throw "请先填写管理员令牌。" }
        $name = Get-BoxText $script:networkNameBox
        $pool = Get-BoxText $script:poolBox
        if ($name -eq "" -or $pool -eq "") { throw "网络名称和 IPv4 地址池不能为空。" }
        $result = Invoke-VpnCtl -Arguments @("network", "create", "--name", $name, "--pool", $pool)
        $network = Convert-ResultToJson $result "创建网络"
        $script:networkIdBox.Text = [string]$network.id
        Add-UiLog "网络已创建：$($network.name)，Network ID：$($network.id)；成员加入时将从 $pool 随机分配虚拟 IPv4。"
        Set-UiStatus "网络已创建" ([System.Drawing.Color]::ForestGreen)
    } catch {
        Add-UiLog "创建网络失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("创建网络失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    }
}

function Create-Invite {
    param([Parameter(Mandatory = $true)][ValidateSet("房主", "成员")][string]$Role)
    try {
        $null = Assert-ControlUrl
        if ((Get-BoxText $script:adminTokenBox) -eq "") { throw "请先填写管理员令牌。" }
        $networkId = Get-BoxText $script:networkIdBox
        $expiry = Get-BoxText $script:expiryBox
        if ($networkId -eq "") { throw "请先创建网络，或填写已有 Network ID。" }
        if ($expiry -eq "") { $expiry = "24h" }
        $result = Invoke-VpnCtl -Arguments @("invite", "create", "--network", $networkId, "--expires", $expiry) -SuppressStandardOutput
        $inviteResult = Convert-ResultToJson $result "生成$Role邀请"
        if ($Role -eq "房主") { $script:hostInviteBox.Text = [string]$inviteResult.token }
        else { $script:memberInviteBox.Text = [string]$inviteResult.token }
        Add-UiLog "$Role邀请已生成，令牌已放入专用框；日志不会记录令牌正文。"
        Set-UiStatus "$Role邀请已生成" ([System.Drawing.Color]::ForestGreen)
    } catch {
        Add-UiLog "生成$Role邀请失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("生成" + $Role + "邀请失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    }
}

function Install-NodeService {
    try {
        $url = Assert-ControlUrl
        $installScript = Join-Path $PackageDirectory "install.ps1"
        if (!(Test-Path -LiteralPath $installScript -PathType Leaf)) { throw "载荷中缺少 install.ps1。" }
        $arguments = @("-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", $installScript, "-PackageDirectory", $PackageDirectory, "-ControlUrl", $url, "-InstallDirectory", $InstallDirectory, "-DataDirectory", $DataDirectory, "-ServiceName", $ServiceName, "-StartService")
        $spec = @{ FileName = (Get-PowerShellPath); Arguments = $arguments; Source = "安装/更新 IPv6Mesh 服务" }
        $result = Invoke-External @spec
        if ($result.ExitCode -ne 0) { throw "安装脚本返回退出码 $($result.ExitCode)。" }
        Add-UiLog "IPv6Mesh 服务安装并启动完成。"
        Set-UiStatus "节点服务已启动" ([System.Drawing.Color]::ForestGreen)
        return $true
    } catch {
        Add-UiLog "安装节点服务失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("安装节点服务失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
        return $false
    }
}

function Get-NodeStatus {
    try {
        $status = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("status")) "读取节点状态"
        $networkId = [string]$status.network_id
        $virtualIPv4 = [string]$status.virtual_ipv4
        $path = [string]$status.path_state
        $errorText = [string]$status.last_error
        if ($networkId -ne "") { $script:networkIdBox.Text = $networkId }
        $script:nodeStatusLabel.Text = "网络：$networkId    虚拟 IPv4：$virtualIPv4    路径：$path"
        if ($errorText -ne "") { $script:nodeStatusLabel.Text += "    最近错误：$errorText" }
        Add-UiLog "节点状态：Network=$networkId，VirtualIPv4=$virtualIPv4，Path=$path"
        return $status
    } catch {
        Add-UiLog "读取节点状态失败：$($_.Exception.Message)" "错误"
        $script:nodeStatusLabel.Text = "节点服务未连接或尚未加入网络"
        return $null
    }
}

function Join-And-ConnectNode {
    try {
        $null = Assert-ControlUrl
        $device = Get-BoxText $script:deviceNameBox
        if ($device -eq "") { $device = $env:COMPUTERNAME; $script:deviceNameBox.Text = $device }
        if (!(Install-NodeService)) { return }
        $status = Get-NodeStatus
        $networkId = [string]$status.network_id
        if ($networkId -eq "") {
            $inviteValue = Get-BoxText $script:nodeInviteBox
            if ($inviteValue -eq "") { throw "请填写当前角色对应的一次性邀请令牌。" }
            $joined = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("join", "--invite", $inviteValue, "--name", $device)) "加入网络"
            $networkId = [string]$joined.network_id
            $script:networkIdBox.Text = $networkId
            Add-UiLog "已加入网络：$networkId；虚拟 IPv4：$($joined.virtual_ipv4)"
        } else {
            Add-UiLog "本机已经加入网络 $networkId，本次不重复消耗邀请令牌。"
        }
        if ($networkId -eq "") { throw "无法确定 Network ID。" }
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", $networkId)) "连接虚拟网络"
        $null = Get-NodeStatus
        Set-UiStatus "节点已连接" ([System.Drawing.Color]::ForestGreen)
    } catch {
        Add-UiLog "加入或连接网络失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("加入或连接网络失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    }
}

function Connect-Node {
    try {
        $networkId = Get-BoxText $script:networkIdBox
        if ($networkId -eq "") { $networkId = [string](Get-NodeStatus).network_id }
        if ($networkId -eq "") { throw "请先加入网络。" }
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", $networkId)) "连接虚拟网络"
        $null = Get-NodeStatus
        Set-UiStatus "节点已连接" ([System.Drawing.Color]::ForestGreen)
    } catch {
        Add-UiLog "连接失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("连接失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    }
}

function Disconnect-Node {
    try {
        $networkId = Get-BoxText $script:networkIdBox
        if ($networkId -eq "") { throw "请先填写或读取 Network ID。" }
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("disconnect", "--network", $networkId)) "断开虚拟网络"
        $null = Get-NodeStatus
        Set-UiStatus "节点已断开" ([System.Drawing.Color]::DarkOrange)
    } catch {
        Add-UiLog "断开失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("断开失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    }
}

function Leave-Node {
    try {
        $networkId = Get-BoxText $script:networkIdBox
        if ($networkId -eq "") { throw "请先填写或读取 Network ID。" }
        $null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("leave", "--network", $networkId)) "离开网络"
        $script:networkIdBox.Text = ""
        $script:nodeStatusLabel.Text = "本机尚未加入网络"
        Add-UiLog "本机已离开网络；再次加入需要新的邀请令牌。"
        Set-UiStatus "节点未加入网络" ([System.Drawing.Color]::DarkOrange)
    } catch {
        Add-UiLog "离开网络失败：$($_.Exception.Message)" "错误"
        [void][System.Windows.Forms.MessageBox]::Show($script:form, ("离开网络失败：" + [Environment]::NewLine + $_.Exception.Message), "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
    }
}

function Copy-UiField {
    param([Parameter(Mandatory = $true)][System.Windows.Forms.TextBox]$Box, [Parameter(Mandatory = $true)][string]$Description)
    $value = Get-BoxText $Box
    if ($value -eq "") {
        [void][System.Windows.Forms.MessageBox]::Show($script:form, "$Description 为空。", "IPv6Mesh", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Information)
        return
    }
    [System.Windows.Forms.Clipboard]::SetText($value)
    Add-UiLog "已复制 $Description；日志不会记录其正文。"
}

function Export-UiLog {
    $dialog = New-Object System.Windows.Forms.SaveFileDialog
    $dialog.Filter = "日志文件 (*.log)|*.log|文本文件 (*.txt)|*.txt|所有文件 (*.*)|*.*"
    $dialog.FileName = "ipv6mesh-debug-{0}.log" -f (Get-Date -Format "yyyyMMdd-HHmmss")
    if ($dialog.ShowDialog($script:form) -ne [System.Windows.Forms.DialogResult]::OK) { return }
    try {
        [IO.File]::WriteAllText($dialog.FileName, ($script:logLines -join [Environment]::NewLine) + [Environment]::NewLine, (New-Object System.Text.UTF8Encoding($false)))
        Add-UiLog "日志已导出：$($dialog.FileName)"
    } catch { Add-UiLog "导出日志失败：$($_.Exception.Message)" "错误" }
}

function New-Label {
    param([string]$Text, [int]$X, [int]$Y, [int]$Width = 100, [int]$Height = 24)
    $label = New-Object System.Windows.Forms.Label
    $label.Text = $Text
    $label.Location = New-Object System.Drawing.Point($X, $Y)
    $label.Size = New-Object System.Drawing.Size($Width, $Height)
    $label.TextAlign = [System.Drawing.ContentAlignment]::MiddleLeft
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
    param([string]$Text, [int]$X, [int]$Y, [int]$Width = 110, [int]$Height = 28)
    $button = New-Object System.Windows.Forms.Button
    $button.Text = $Text
    $button.Location = New-Object System.Drawing.Point($X, $Y)
    $button.Size = New-Object System.Drawing.Size($Width, $Height)
    return $button
}

$initialIPv6 = Get-DetectedIPv6Address
$initialPort = 8080
$initialControlUrl = [string]$ControlUrl
if (![string]::IsNullOrWhiteSpace($ControlUrl)) {
    try {
        $providedUri = $null
        if ([Uri]::TryCreate($ControlUrl, [UriKind]::Absolute, [ref]$providedUri)) {
            if ($providedUri.Port -gt 0) { $initialPort = $providedUri.Port }
            if ($providedUri.Host.Contains(':')) { $initialIPv6 = $providedUri.Host }
        }
    } catch {}
} elseif ($initialIPv6 -ne '') {
    $initialControlUrl = "http://[$initialIPv6]:$initialPort"
}

$script:form = New-Object System.Windows.Forms.Form
$script:form.Text = "IPv6Mesh 远程组网"
$script:form.StartPosition = [System.Windows.Forms.FormStartPosition]::CenterScreen
$script:form.ClientSize = New-Object System.Drawing.Size(1160, 875)
$script:form.MinimumSize = New-Object System.Drawing.Size(1160, 875)
$script:form.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 9)

$title = New-Label "IPv6Mesh 远程组网（中文调试界面）" 15 10 460 30
$title.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 14, [System.Drawing.FontStyle]::Bold)
$script:form.Controls.Add($title)
$script:form.Controls.Add((New-Label "当前角色：" 620 12 75 26))
$script:roleBox = New-Object System.Windows.Forms.ComboBox
$script:roleBox.Location = New-Object System.Drawing.Point(700, 10)
$script:roleBox.Size = New-Object System.Drawing.Size(170, 28)
$script:roleBox.DropDownStyle = [System.Windows.Forms.ComboBoxStyle]::DropDownList
[void]$script:roleBox.Items.AddRange([object[]]@("控制面管理员", "游戏房主", "游戏成员"))
$script:roleBox.SelectedIndex = 0
$script:form.Controls.Add($script:roleBox)
$script:roleHintLabel = New-Label "管理员可启动控制面、创建网络并生成房主/成员一次性邀请。" 15 42 1080 24
$script:roleHintLabel.ForeColor = [System.Drawing.Color]::DimGray
$script:form.Controls.Add($script:roleHintLabel)

$controlGroup = New-Object System.Windows.Forms.GroupBox
$controlGroup.Text = "一、控制面和网络管理（管理员）"
$controlGroup.Location = New-Object System.Drawing.Point(10, 70)
$controlGroup.Size = New-Object System.Drawing.Size(1140, 275)
$script:form.Controls.Add($controlGroup)
$controlGroup.Controls.Add((New-Label "控制面 URL：" 15 27 105 25))
$script:controlUrlBox = New-TextBox 120 24 650
$script:controlUrlBox.Text = $initialControlUrl
$controlGroup.Controls.Add($script:controlUrlBox)
$healthButton = New-Button "检查健康" 785 22 100
$healthButton.Add_Click({ [void](Test-ControlHealth) })
$controlGroup.Controls.Add($healthButton)
$controlGroup.Controls.Add((New-Label "本机 IPv6：" 15 62 105 25))
$script:ipv6AddressBox = New-TextBox 120 59 390
$script:ipv6AddressBox.Text = $initialIPv6
$controlGroup.Controls.Add($script:ipv6AddressBox)
$detectIPv6Button = New-Button "自动检测" 520 56 100
$detectIPv6Button.Add_Click({ Refresh-LocalIPv6 })
$controlGroup.Controls.Add($detectIPv6Button)
$controlGroup.Controls.Add((New-Label "端口：" 635 62 45 25))
$script:portBox = New-TextBox 680 59 80
$script:portBox.Text = [string]$initialPort
$controlGroup.Controls.Add($script:portBox)
$controlGroup.Controls.Add((New-Label "监听地址：" 15 97 105 25))
$script:listenAddressBox = New-TextBox 120 94 230 -ReadOnly
$script:listenAddressBox.Text = "[::]:$initialPort"
$controlGroup.Controls.Add($script:listenAddressBox)
$controlGroup.Controls.Add((New-Label "管理员令牌：" 370 97 105 25))
$script:adminTokenBox = New-TextBox 475 94 300 -Password
$controlGroup.Controls.Add($script:adminTokenBox)
$startControlButton = New-Button "启动控制面" 790 92 110
$startControlButton.Add_Click({ Start-ControlPlane })
$controlGroup.Controls.Add($startControlButton)
$stopControlButton = New-Button "停止控制面" 910 92 110
$stopControlButton.Add_Click({ Stop-ControlPlane; Set-UiStatus "控制面已停止" ([System.Drawing.Color]::DarkOrange) })
$controlGroup.Controls.Add($stopControlButton)
$controlGroup.Controls.Add((New-Label "网络名称：" 15 132 105 25))
$script:networkNameBox = New-TextBox 120 129 220
$script:networkNameBox.Text = "friends-steam"
$controlGroup.Controls.Add($script:networkNameBox)
$controlGroup.Controls.Add((New-Label "IPv4 地址池：" 360 132 100 25))
$script:poolBox = New-TextBox 460 129 170
$script:poolBox.Text = "10.42.0.0/24"
$controlGroup.Controls.Add($script:poolBox)
$controlGroup.Controls.Add((New-Label "邀请有效期：" 650 132 100 25))
$script:expiryBox = New-TextBox 750 129 90
$script:expiryBox.Text = "24h"
$controlGroup.Controls.Add($script:expiryBox)
$createNetworkButton = New-Button "创建网络" 860 126 110
$createNetworkButton.Add_Click({ Create-Network })
$controlGroup.Controls.Add($createNetworkButton)
$controlGroup.Controls.Add((New-Label "Network ID：" 15 167 105 25))
$script:networkIdBox = New-TextBox 120 164 650
$script:networkIdBox.Text = $Network
$controlGroup.Controls.Add($script:networkIdBox)
$controlGroup.Controls.Add((New-Label "加入时由控制面随机分配虚拟 IPv4，分配后保持不变。" 785 167 330 38))
$controlGroup.Controls.Add((New-Label "房主邀请：" 15 202 105 25))
$script:hostInviteBox = New-TextBox 120 199 650 -Password -ReadOnly
$controlGroup.Controls.Add($script:hostInviteBox)
$hostInviteButton = New-Button "生成房主邀请" 785 196 125
$hostInviteButton.Add_Click({ Create-Invite "房主" })
$controlGroup.Controls.Add($hostInviteButton)
$copyHostButton = New-Button "复制房主令牌" 920 196 125
$copyHostButton.Add_Click({ Copy-UiField $script:hostInviteBox "房主邀请令牌" })
$controlGroup.Controls.Add($copyHostButton)
$controlGroup.Controls.Add((New-Label "成员邀请：" 15 237 105 25))
$script:memberInviteBox = New-TextBox 120 234 650 -Password -ReadOnly
$controlGroup.Controls.Add($script:memberInviteBox)
$memberInviteButton = New-Button "生成成员邀请" 785 231 125
$memberInviteButton.Add_Click({ Create-Invite "成员" })
$controlGroup.Controls.Add($memberInviteButton)
$copyMemberButton = New-Button "复制成员令牌" 920 231 125
$copyMemberButton.Add_Click({ Copy-UiField $script:memberInviteBox "成员邀请令牌" })
$controlGroup.Controls.Add($copyMemberButton)

$nodeGroup = New-Object System.Windows.Forms.GroupBox
$nodeGroup.Text = "二、房主/成员节点操作"
$nodeGroup.Location = New-Object System.Drawing.Point(10, 360)
$nodeGroup.Size = New-Object System.Drawing.Size(1140, 165)
$script:form.Controls.Add($nodeGroup)
$nodeGroup.Controls.Add((New-Label "当前角色邀请：" 15 27 105 25))
$script:nodeInviteBox = New-TextBox 120 24 650 -Password
$script:nodeInviteBox.Text = $Invite
$nodeGroup.Controls.Add($script:nodeInviteBox)
$nodeGroup.Controls.Add((New-Label "设备名：" 15 62 105 25))
$script:deviceNameBox = New-TextBox 120 59 300
$script:deviceNameBox.Text = if ([string]::IsNullOrWhiteSpace($DeviceName)) { $env:COMPUTERNAME } else { $DeviceName }
$nodeGroup.Controls.Add($script:deviceNameBox)
$script:nodeActionButton = New-Button "安装并加入 VPN" 445 56 145
$script:nodeActionButton.Add_Click({ Join-And-ConnectNode })
$nodeGroup.Controls.Add($script:nodeActionButton)
$refreshButton = New-Button "刷新状态" 605 56 100
$refreshButton.Add_Click({ [void](Get-NodeStatus) })
$nodeGroup.Controls.Add($refreshButton)
$connectButton = New-Button "连接" 720 56 80
$connectButton.Add_Click({ Connect-Node })
$nodeGroup.Controls.Add($connectButton)
$disconnectButton = New-Button "断开" 815 56 80
$disconnectButton.Add_Click({ Disconnect-Node })
$nodeGroup.Controls.Add($disconnectButton)
$leaveButton = New-Button "离开网络" 910 56 100
$leaveButton.Add_Click({ Leave-Node })
$nodeGroup.Controls.Add($leaveButton)
$script:nodeStatusLabel = New-Label "节点尚未检查状态" 15 97 1080 25
$script:nodeStatusLabel.ForeColor = [System.Drawing.Color]::DimGray
$nodeGroup.Controls.Add($script:nodeStatusLabel)

$logGroup = New-Object System.Windows.Forms.GroupBox
$logGroup.Text = "三、实时调试日志（邀请令牌不会写入日志）"
$logGroup.Location = New-Object System.Drawing.Point(10, 535)
$logGroup.Size = New-Object System.Drawing.Size(1140, 300)
$script:form.Controls.Add($logGroup)
$script:logBox = New-Object System.Windows.Forms.TextBox
$script:logBox.Multiline = $true
$script:logBox.ReadOnly = $true
$script:logBox.ScrollBars = [System.Windows.Forms.ScrollBars]::Both
$script:logBox.WordWrap = $false
$script:logBox.Location = New-Object System.Drawing.Point(15, 25)
$script:logBox.Size = New-Object System.Drawing.Size(1110, 220)
$script:logBox.Font = New-Object System.Drawing.Font("Consolas", 9)
$logGroup.Controls.Add($script:logBox)
$clearLogButton = New-Button "清空日志" 15 255 100
$clearLogButton.Add_Click({ $script:logLines.Clear(); $script:logBox.Clear(); Add-UiLog "日志已清空。" })
$logGroup.Controls.Add($clearLogButton)
$copyLogButton = New-Button "复制日志" 130 255 100
$copyLogButton.Add_Click({ [System.Windows.Forms.Clipboard]::SetText(($script:logLines -join [Environment]::NewLine)); Add-UiLog "日志已复制到剪贴板。" })
$logGroup.Controls.Add($copyLogButton)
$exportLogButton = New-Button "导出日志" 245 255 100
$exportLogButton.Add_Click({ Export-UiLog })
$logGroup.Controls.Add($exportLogButton)
$logGroup.Controls.Add((New-Label "提交 debug 信息前，请检查日志和导出文件中没有不应公开的主机信息。" 370 258 700 24))

$script:ipv6AddressBox.Add_TextChanged({
    if ($script:updatingEndpoint) { return }
    try { Update-ControlEndpoint } catch {}
})
$script:portBox.Add_TextChanged({
    if ($script:updatingEndpoint) { return }
    try { Update-ControlEndpoint } catch {}
})

$script:roleBox.Add_SelectedIndexChanged({
    switch ([string]$script:roleBox.SelectedItem) {
        "控制面管理员" {
            $script:roleHintLabel.Text = "管理员可启动控制面、创建网络并生成房主/成员一次性邀请；需要管理员令牌。"
            $script:nodeInviteBox.Enabled = $false
            $script:nodeActionButton.Enabled = $false
        }
        "游戏房主" {
            $script:roleHintLabel.Text = "房主使用房主专属邀请加入，并在游戏中创建房间；不要使用成员邀请或管理员令牌。"
            $script:nodeInviteBox.Enabled = $true
            $script:nodeActionButton.Enabled = $true
            if ($script:hostInviteBox.Text -ne "") { $script:nodeInviteBox.Text = $script:hostInviteBox.Text }
        }
        "游戏成员" {
            $script:roleHintLabel.Text = "成员使用成员专属邀请加入，然后使用房主显示的虚拟 IPv4 或游戏邀请进入房间。"
            $script:nodeInviteBox.Enabled = $true
            $script:nodeActionButton.Enabled = $true
            if ($script:memberInviteBox.Text -ne "") { $script:nodeInviteBox.Text = $script:memberInviteBox.Text }
        }
    }
})
$script:nodeInviteBox.Enabled = $false
$script:nodeActionButton.Enabled = $false
$script:form.Add_FormClosing({ Stop-AllResources })

Add-UiLog "IPv6Mesh 中文 UI $Version 已启动。"
Add-UiLog "请先选择角色；管理员先启动控制面并创建网络，房主/成员再安装并加入。"
Add-UiLog "当前 UI 不会把管理员令牌和一次性邀请令牌写入日志。"
if ($initialIPv6 -ne '') {
    Add-UiLog "已检测本机 IPv6：$initialIPv6；默认控制面 URL：$initialControlUrl"
} else {
    Add-UiLog "未检测到可用的全局 IPv6；请点击“自动检测”或手动填写 IPv6。" "警告"
}
Add-UiLog "手动关闭窗口时会停止本机 IPv6Mesh 服务、清理虚拟网卡/地址/路由，并停止本窗口启动的控制面。"
Set-UiStatus "等待操作" ([System.Drawing.Color]::MidnightBlue)
try {
    [void][System.Windows.Forms.Application]::Run($script:form)
} finally {
    Stop-AllResources
}
