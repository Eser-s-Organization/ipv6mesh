//go:build windows

package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

func TestResolveControlURL(t *testing.T) {
	valid := []string{
		"http://[2001:db8::1]:8080",
		"https://control.example.test:8443",
	}
	for _, value := range valid {
		if got, err := resolveControlURL(value); err != nil || got != value {
			t.Fatalf("resolveControlURL(%q) = %q, %v", value, got, err)
		}
	}
	invalid := []string{"", "2001:db8::1", "ftp://control.example.test", "http://"}
	for _, value := range invalid[1:] {
		if _, err := resolveControlURL(value); err == nil {
			t.Fatalf("resolveControlURL(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSafeZipPath(t *testing.T) {
	for _, value := range []string{"install.ps1", "nested\\file.txt", "nested/file.txt"} {
		if _, err := safeZipPath(value); err != nil {
			t.Fatalf("safeZipPath(%q) failed: %v", value, err)
		}
	}
	for _, value := range []string{"..\\escape.txt", "../escape.txt", "C:\\escape.txt", "\\\\server\\share\\escape.txt"} {
		if _, err := safeZipPath(value); err == nil {
			t.Fatalf("safeZipPath(%q) unexpectedly succeeded", value)
		}
	}
}

func TestPromptValueUsesFallback(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	value, err := promptValue(reader, "Device name", "", "test-device", false)
	if err != nil {
		t.Fatalf("promptValue returned error: %v", err)
	}
	if value != "test-device" {
		t.Fatalf("promptValue = %q, want fallback", value)
	}
}

func TestPromptValueRejectsMissingNonInteractiveValue(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	if _, err := promptValue(reader, "Invite", "", "", true); err == nil {
		t.Fatal("promptValue unexpectedly accepted a missing non-interactive value")
	}
}

func TestServiceResponseErrorIncludesCodeAndMessage(t *testing.T) {
	err := serviceResponseError("join", ipc.Response{Error: &ipc.Error{Code: "control_failed", Message: "control plane unavailable"}})
	if err == nil || !strings.Contains(err.Error(), "control_failed") || !strings.Contains(err.Error(), "control plane unavailable") {
		t.Fatalf("serviceResponseError = %v", err)
	}
}

func TestBuildInstallArgumentsPassesPackageDirectory(t *testing.T) {
	args := buildInstallArguments(installerOptions{startService: true}, `C:\Temp\ipv6mesh-installer`, "http://[2001:db8::1]:8080")
	for i, arg := range args {
		if arg == "-PackageDirectory" {
			if i+1 >= len(args) {
				t.Fatal("-PackageDirectory has no value")
			}
			if args[i+1] != `C:\Temp\ipv6mesh-installer` {
				t.Fatalf("-PackageDirectory value = %q, want extracted package directory", args[i+1])
			}
			return
		}
	}
	t.Fatal("install arguments do not include -PackageDirectory")
}

func TestBuildGraphicalArgumentsPassesUIPayloadAndInitialValues(t *testing.T) {
	args := buildGraphicalArguments(installerOptions{
		controlURL:       "http://[2001:db8::1]:8080",
		invite:           "invite-value",
		deviceName:       "device-a",
		networkID:        "network-a",
		installDirectory: `C:\Program Files\IPv6Mesh`,
	}, `C:\Temp\ipv6mesh-installer`, "0.1.0-debug.5")
	want := []string{
		"-STA",
		"-WindowStyle", "Hidden",
		filepath.Join(`C:\Temp\ipv6mesh-installer`, "ui.ps1"),
		"-PackageDirectory", `C:\Temp\ipv6mesh-installer`,
		"-Version", "0.1.0-debug.5",
		"-ControlUrl", "http://[2001:db8::1]:8080",
		"-Invite", "invite-value",
		"-DeviceName", "device-a",
		"-Network", "network-a",
	}
	for _, value := range want {
		found := false
		for _, arg := range args {
			if arg == value {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("graphical arguments missing %q: %v", value, args)
		}
	}
}

func TestWindowsPackageIncludesChineseUI(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	packageRoot := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows")
	buildScript, err := os.ReadFile(filepath.Join(packageRoot, "build.ps1"))
	if err != nil {
		t.Fatalf("read build script: %v", err)
	}
	if !strings.Contains(string(buildScript), `"ui.ps1"`) {
		t.Fatal("build script does not include ui.ps1")
	}
	uiScript, err := os.ReadFile(filepath.Join(packageRoot, "ui.ps1"))
	if err != nil {
		t.Fatalf("read UI script: %v", err)
	}
	if !bytes.HasPrefix(uiScript, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("UI script must be UTF-8 with BOM for Windows PowerShell 5.1")
	}
	contents := string(uiScript)
	for _, required := range []string{
		"System.Windows.Forms",
		"你想做什么？",
		"创建网络",
		"加入网络",
		"Show-WelcomePage",
		"Show-HostPage",
		"Show-MemberPage",
		"Start-HostRoom",
		"Join-MemberRoom",
		"重新检测",
		"复制房主 IPv6",
		"房主虚拟 IPv4",
		"本机虚拟 IPv4",
		"诊断与日志",
		"节点状态读取已恢复",
		"Set-PrimaryBusy",
		"Stop-AllResources",
		"Get-NetIPAddress -AddressFamily IPv6",
		"Update-ControlEndpoint",
		"Stop-NodeService",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("UI script missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"控制面管理员",
		"游戏房主",
		"游戏成员",
		"管理员令牌：",
		"房主邀请：",
		"成员邀请：",
		"复制 Network ID",
		"随机生成房主邀请",
		"随机生成成员邀请",
	} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("normal room UI still exposes %q", forbidden)
		}
	}
}

func TestWindowsDocumentationDescribesPersistentLiveDiagnostics(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Join(filepath.Dir(sourceFile), "..", "..")
	for _, name := range []string{"README.md", filepath.Join("packaging", "windows", "README.md")} {
		contents, err := os.ReadFile(filepath.Join(repositoryRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := strings.Join(strings.Fields(string(contents)), " ")
		for _, required := range []string{"always visible", "every two seconds", "does not repeat unchanged status"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing diagnostics statement %q", name, required)
			}
		}
	}
}

func TestWindowsUIUsesRoomWorkflowAndActionableHealthCheck(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	uiScript, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatalf("read UI script: %v", err)
	}
	contents := string(uiScript)
	for _, required := range []string{
		"New-RandomToken",
		"CONTROL_BOOTSTRAP_TOKEN",
		"CONTROL_ROOM_MODE",
		"CONTROL_REPOSITORY_MODE",
		"Wait-ControlPlaneReady",
		"Get-WebException",
		"Stop-ControlPlane",
		"Stop-AllResources",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("UI script missing %q", required)
		}
		for _, forbidden := range []string{
			"控制面管理员",
			"游戏房主",
			"游戏成员",
			"管理员令牌：",
			"房主邀请：",
			"成员邀请：",
			"复制 Network ID",
			"随机生成房主邀请",
			"随机生成成员邀请",
		} {
			if strings.Contains(contents, forbidden) {
				t.Fatalf("normal room UI still exposes %q", forbidden)
			}
		}
	}
	packagedIndex := strings.Index(contents, "$packaged = Join-Path $PackageDirectory $Name")
	installedIndex := strings.Index(contents, "$installed = Join-Path $InstallDirectory $Name")
	if packagedIndex < 0 || installedIndex < 0 || packagedIndex > installedIndex {
		t.Fatalf("UI must prefer the current packaged executable over a stale installed executable")
	}
}

func TestWindowsUIDoesNotDoubleQuoteSmartQuoteLogText(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	quotedPath := strings.ReplaceAll(uiPath, "'", "''")
	command := `
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    $parseErrors | ForEach-Object { Write-Error $_.Message }
    exit 1
}
$badCommands = $ast.FindAll({
    param($node)
    if (-not ($node -is [System.Management.Automation.Language.CommandAst]) -or $node.GetCommandName() -ne 'Add-UiLog') {
        return $false
    }
    for ($index = 1; $index -lt $node.CommandElements.Count; $index++) {
        $element = $node.CommandElements[$index]
        $doubleQuoted = $element -is [System.Management.Automation.Language.StringConstantExpressionAst] -and
            $element.StringConstantType -eq 'DoubleQuoted'
        $expandable = $element -is [System.Management.Automation.Language.ExpandableStringExpressionAst]
        if (($doubleQuoted -or $expandable) -and
            $element.Extent.Text -match '[“”]') {
            return $true
        }
    }
    return $false
}, $true)
if ($badCommands.Count -gt 0) {
    $badCommands | ForEach-Object { Write-Error ("Add-UiLog at line " + $_.Extent.StartLineNumber + " uses smart quotes in a double-quoted argument") }
    exit 1
}
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell smart-quote regression check failed: %v\n%s", err, output)
	}
}

func TestWindowsUIAcceptsMissingGlobalIPv6DuringStartup(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	quotedPath := strings.ReplaceAll(uiPath, "'", "''")
	command := `
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    $parseErrors | ForEach-Object { Write-Error $_.Message }
    exit 1
}
$function = $ast.FindAll({
    param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Test-GlobalIPv6'
}, $true) | Select-Object -First 1
if ($null -eq $function) {
    Write-Error 'Test-GlobalIPv6 function not found'
    exit 1
}
. ([scriptblock]::Create($function.Extent.Text))
try {
    $result = Test-GlobalIPv6 -Value ''
} catch {
    Write-Error $_.Exception.Message
    exit 1
}
if ($result -ne $false) {
    Write-Error ('Test-GlobalIPv6 empty input returned ' + $result)
    exit 1
}
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell empty-IPv6 regression check failed: %v\n%s", err, output)
	}
}

func TestWindowsUIKeepsDiagnosticsVisibleOnOperationPages(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	uiScript, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatalf("read UI script: %v", err)
	}
	contents := string(uiScript)

	for _, forbidden := range []string{
		`function Toggle-Diagnostics`,
		`New-Button "显示诊断与日志"`,
		`$script:diagnosticsPanel.Visible = !$script:diagnosticsPanel.Visible`,
	} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("UI still contains collapsible diagnostics behavior %q", forbidden)
		}
	}

	for _, required := range []string{
		`$script:activePage = $Name`,
		`$script:diagnosticsPanel.Visible = ($Name -ne "Welcome")`,
		`$script:diagnosticsPanel.BringToFront()`,
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("UI missing persistent diagnostics behavior %q", required)
		}
	}
}

func TestWindowsUIStatusLogDecision(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	quotedPath := strings.ReplaceAll(uiPath, "'", "''")
	command := `
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    $parseErrors | ForEach-Object { Write-Error $_.Message }
    exit 1
}
$function = $ast.FindAll({
    param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Get-StatusLogDecision'
}, $true) | Select-Object -First 1
if ($null -eq $function) {
    Write-Error 'Get-StatusLogDecision function not found'
    exit 1
}
. ([scriptblock]::Create($function.Extent.Text))
$cases = @(
    @{ Name = 'first success'; Parameters = @{ Automatic = $true; Succeeded = $true; Fingerprint = '10.42.0.1|Direct|'; HasPrevious = $false; PreviousSucceeded = $false; PreviousFingerprint = '' }; Want = 'Changed' },
    @{ Name = 'unchanged success'; Parameters = @{ Automatic = $true; Succeeded = $true; Fingerprint = '10.42.0.1|Direct|'; HasPrevious = $true; PreviousSucceeded = $true; PreviousFingerprint = '10.42.0.1|Direct|' }; Want = 'None' },
    @{ Name = 'changed success'; Parameters = @{ Automatic = $true; Succeeded = $true; Fingerprint = '10.42.0.1|Relay|'; HasPrevious = $true; PreviousSucceeded = $true; PreviousFingerprint = '10.42.0.1|Direct|' }; Want = 'Changed' },
    @{ Name = 'first failure'; Parameters = @{ Automatic = $true; Succeeded = $false; Fingerprint = ''; HasPrevious = $false; PreviousSucceeded = $false; PreviousFingerprint = '' }; Want = 'Failed' },
    @{ Name = 'failure after success'; Parameters = @{ Automatic = $true; Succeeded = $false; Fingerprint = ''; HasPrevious = $true; PreviousSucceeded = $true; PreviousFingerprint = '10.42.0.1|Direct|' }; Want = 'Failed' },
    @{ Name = 'repeated failure'; Parameters = @{ Automatic = $true; Succeeded = $false; Fingerprint = ''; HasPrevious = $true; PreviousSucceeded = $false; PreviousFingerprint = '' }; Want = 'None' },
    @{ Name = 'recovery'; Parameters = @{ Automatic = $true; Succeeded = $true; Fingerprint = '10.42.0.1|Direct|'; HasPrevious = $true; PreviousSucceeded = $false; PreviousFingerprint = '' }; Want = 'Recovered' },
    @{ Name = 'manual unchanged'; Parameters = @{ Automatic = $false; Succeeded = $true; Fingerprint = '10.42.0.1|Direct|'; HasPrevious = $true; PreviousSucceeded = $true; PreviousFingerprint = '10.42.0.1|Direct|' }; Want = 'Manual' }
)
foreach ($case in $cases) {
    $parameters = $case.Parameters
    $got = Get-StatusLogDecision @parameters
    if ($got -ne $case.Want) {
        Write-Error ($case.Name + ': got ' + $got + ', want ' + $case.Want)
        exit 1
    }
}
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell status-log decision check failed: %v\n%s", err, output)
	}
}

func TestWindowsUILiveStatusTimerUsesQuietDeduplicatedPolling(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	uiScript, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatalf("read UI script: %v", err)
	}
	contents := string(uiScript)
	for _, required := range []string{
		`$script:statusRefreshTimer = New-Object System.Windows.Forms.Timer`,
		`$script:statusRefreshTimer.Interval = 2000`,
		`$script:statusRefreshTimer.Add_Tick({ Invoke-AutomaticStatusRefresh })`,
		`function Start-StatusRefresh`,
		`function Stop-StatusRefresh`,
		`function Dispose-StatusRefreshTimer`,
		`function Invoke-AutomaticStatusRefresh`,
		`$script:primaryBusy -or $script:cleanupStarted`,
		`$script:activePage -eq "Welcome" -or $script:statusRefreshInProgress`,
		`Get-NodeStatus -Automatic`,
		`Invoke-VpnCtl -Arguments @("status") -SuppressStandardOutput -Quiet:$Automatic`,
		`Convert-ResultToJson $result "读取节点状态" -Quiet:$Automatic`,
		`Get-StatusLogDecision`,
		`Dispose-StatusRefreshTimer`,
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("UI missing live status behavior %q", required)
		}
	}

	cleanupIndex := strings.Index(contents, "function Stop-AllResources")
	if cleanupIndex < 0 {
		t.Fatal("Stop-AllResources function not found")
	}
	disposeIndex := strings.Index(contents[cleanupIndex:], "Dispose-StatusRefreshTimer")
	resourceIndex := strings.Index(contents[cleanupIndex:], "Stop-StartedResources")
	if disposeIndex < 0 || resourceIndex < 0 || disposeIndex > resourceIndex {
		t.Fatalf("status timer must be disposed before resources stop (cleanup=%d dispose=%d resources=%d)", cleanupIndex, disposeIndex, resourceIndex)
	}
}

func TestInstallScriptStopsExistingServiceBeforeCopyingFiles(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	scriptPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "install.ps1")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read install script: %v", err)
	}
	contents := string(script)
	stopIndex := strings.Index(contents, "Stop-Service")
	copyIndex := strings.Index(contents, "Copy-Item -LiteralPath")
	if stopIndex < 0 {
		t.Fatal("install script does not stop an existing service")
	}
	if copyIndex < 0 {
		t.Fatal("install script does not copy package files")
	}
	if stopIndex > copyIndex {
		t.Fatalf("Stop-Service occurs after Copy-Item (stop=%d, copy=%d)", stopIndex, copyIndex)
	}
}
