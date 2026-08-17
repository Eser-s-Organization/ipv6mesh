//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
		for _, required := range []string{
			"always visible",
			"every two seconds",
			"does not repeat unchanged status",
			"window can be resized",
			"drag the horizontal diagnostics divider",
			"operation area scrolls",
		} {
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

func TestWindowsUIUsesManagedOperationShell(t *testing.T) {
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
		`$rootLayout = New-Object System.Windows.Forms.TableLayoutPanel`,
		`$rootLayout.Dock = [System.Windows.Forms.DockStyle]::Fill`,
		`$script:contentPanel = New-Object System.Windows.Forms.Panel`,
		`$script:operationShell = New-Object System.Windows.Forms.Panel`,
		`$script:operationSplit = New-Object System.Windows.Forms.SplitContainer`,
		`$script:operationSplit.Orientation = [System.Windows.Forms.Orientation]::Horizontal`,
		`$script:operationSplit.Panel1.AutoScroll = $true`,
		`$script:operationSplit.Panel2.Controls.Add($script:diagnosticsPanel)`,
		`$script:diagnosticsPanel.Visible = $true`,
		`$script:operationShell.Visible = ($Name -ne "Welcome")`,
		`$script:form.AutoScaleMode = [System.Windows.Forms.AutoScaleMode]::Dpi`,
		`Set-ResponsiveWindowBounds $script:form`,
		`$script:statusRefreshTimer.Interval = 2000`,
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("UI missing managed operation-shell behavior %q", required)
		}
	}
	for _, forbidden := range []string{
		`$script:diagnosticsPanel.BringToFront()`,
		`$script:diagnosticsPanel.Visible = ($Name -ne "Welcome")`,
		`$script:form.Controls.Add($script:diagnosticsPanel)`,
	} {
		if strings.Contains(contents, forbidden) {
			t.Errorf("UI retains form-level diagnostics workaround %q", forbidden)
		}
	}
}

func TestWindowsUIOperationPagesUseResponsiveGrids(t *testing.T) {
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
		`function New-ResponsivePageGrid`,
		`$script:hostPanel = New-ResponsivePageGrid "HostPanel"`,
		`$script:memberPanel = New-ResponsivePageGrid "MemberPanel"`,
		`$page.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))`,
		`$page.MinimumSize = New-Object System.Drawing.Size(620, 0)`,
		`$script:ipv6AddressBox.Dock = [System.Windows.Forms.DockStyle]::Fill`,
		`$script:memberHostIPv6Box.Dock = [System.Windows.Forms.DockStyle]::Fill`,
		`$script:operationSplit.Panel1.AutoScroll = $true`,
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("UI missing responsive operation-page behavior %q", required)
		}
	}
	for _, forbidden := range []string{
		`New-TextBox 170 82`,
		`New-Button "创建并连接" 40 275`,
		`New-Button "加入并连接" 40 275`,
		`$script:hostPanel.Size = New-Object System.Drawing.Size(1080, 570)`,
		`$script:memberPanel.Size = New-Object System.Drawing.Size(1080, 570)`,
	} {
		if strings.Contains(contents, forbidden) {
			t.Errorf("UI retains fixed operation-page layout %q", forbidden)
		}
	}
}

func TestWindowsUIDiagnosticsUsesFillAndWrappingLayout(t *testing.T) {
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
		`$diagnosticsLayout = New-Object System.Windows.Forms.TableLayoutPanel`,
		`$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 100)))`,
		`$statusActions.WrapContents = $true`,
		`$logActions.WrapContents = $true`,
		`$script:logBox.Dock = [System.Windows.Forms.DockStyle]::Fill`,
		`$script:logBox.MinimumSize = New-Object System.Drawing.Size(200, 80)`,
		`$script:operationSplit.Panel2.Controls.Add($script:diagnosticsPanel)`,
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("UI missing responsive diagnostics behavior %q", required)
		}
	}
	for _, forbidden := range []string{
		`$script:diagnosticsPanel.Location = New-Object System.Drawing.Point(40, 350)`,
		`$script:diagnosticsPanel.Size = New-Object System.Drawing.Size(1040, 290)`,
		`$script:logBox.Location = New-Object System.Drawing.Point(20, 100)`,
		`$script:logBox.Size = New-Object System.Drawing.Size(1000, 145)`,
		`function New-Label`,
		`function New-TextBox`,
		`function New-Button`,
	} {
		if strings.Contains(contents, forbidden) {
			t.Errorf("UI retains fixed diagnostics layout %q", forbidden)
		}
	}
}

func TestWindowsUIResponsiveLayoutAudit(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	cmd := exec.Command(
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-File", uiPath,
		"-PackageDirectory", t.TempDir(),
		"-LayoutAudit",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("responsive WinForms layout audit failed: %v\n%s", err, output)
	}
	compact := strings.ReplaceAll(strings.ReplaceAll(string(output), "\r", ""), "\n", "")
	if !strings.Contains(compact, `"Passed":true`) {
		t.Fatalf("responsive WinForms layout audit did not report success:\n%s", output)
	}
	for _, required := range []string{`"Case":"preferred"`, `"Case":"minimum"`, `"Case":"large"`, `"Case":"constrained"`, `"Case":"large-font"`, `"Case":"upper-limit"`, `"Case":"lower-limit"`, `"MemberLayoutMode":"Wide"`, `"MemberLayoutMode":"Narrow"`, `"MemberPanelWidth":`, `"MemberGridWidth":`, `"SettingsMemberOverlap":0`, `"SplitterPreserved":true`} {
		if !strings.Contains(compact, required) {
			t.Errorf("responsive WinForms layout audit missing sample %s", required)
		}
	}
}

func TestWindowsUIMemberLogDecision(t *testing.T) {
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
if ($parseErrors.Count -gt 0) { exit 1 }
$function = $ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-MemberLogDecision' }, $true) | Select-Object -First 1
if ($null -eq $function) { Write-Error 'Get-MemberLogDecision function not found'; exit 1 }
. ([scriptblock]::Create($function.Extent.Text))
$cases = @(
    @{ Name = 'first success'; Parameters = @{ Automatic = $true; Succeeded = $true; Fingerprint = 'HOST-PC|10.42.0.2|True|online'; HasPrevious = $false; PreviousSucceeded = $false; PreviousFingerprint = '' }; Want = 'Changed' },
    @{ Name = 'unchanged success'; Parameters = @{ Automatic = $true; Succeeded = $true; Fingerprint = 'HOST-PC|10.42.0.2|True|online'; HasPrevious = $true; PreviousSucceeded = $true; PreviousFingerprint = 'HOST-PC|10.42.0.2|True|online' }; Want = 'None' },
    @{ Name = 'failure'; Parameters = @{ Automatic = $true; Succeeded = $false; Fingerprint = ''; HasPrevious = $true; PreviousSucceeded = $true; PreviousFingerprint = 'x' }; Want = 'Failed' },
    @{ Name = 'repeated failure'; Parameters = @{ Automatic = $true; Succeeded = $false; Fingerprint = ''; HasPrevious = $true; PreviousSucceeded = $false; PreviousFingerprint = '' }; Want = 'None' },
    @{ Name = 'recovery'; Parameters = @{ Automatic = $true; Succeeded = $true; Fingerprint = 'x'; HasPrevious = $true; PreviousSucceeded = $false; PreviousFingerprint = '' }; Want = 'Recovered' }
)
foreach ($case in $cases) {
    $parameters = $case.Parameters
    $got = Get-MemberLogDecision @parameters
    if ($got -ne $case.Want) { throw ($case.Name + ': got ' + $got + ', want ' + $case.Want) }
}
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell member-log decision check failed: %v\n%s", err, output)
	}
}

func TestWindowsUIMemberLayoutMode(t *testing.T) {
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
if ($parseErrors.Count -gt 0) { exit 1 }
$function = $ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-MemberLayoutMode' }, $true) | Select-Object -First 1
if ($null -eq $function) { Write-Error 'Get-MemberLayoutMode function not found'; exit 1 }
. ([scriptblock]::Create($function.Extent.Text))
if ((Get-MemberLayoutMode -AvailableWidth 1120 -SettingsPreferredWidth 620 -MembersMinimumWidth 300 -Gap 16) -ne 'Wide') { throw 'wide failed' }
if ((Get-MemberLayoutMode -AvailableWidth 900 -SettingsPreferredWidth 620 -MembersMinimumWidth 300 -Gap 16) -ne 'Narrow') { throw 'narrow failed' }
if ((Get-MemberLayoutMode -AvailableWidth 0 -SettingsPreferredWidth 620 -MembersMinimumWidth 300 -Gap 16) -ne 'Narrow') { throw 'zero failed' }
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell member-layout mode check failed: %v\n%s", err, output)
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

func TestWindowsUISplitLayoutDecision(t *testing.T) {
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
        $node.Name -eq 'Get-SplitLayoutDecision'
}, $true) | Select-Object -First 1
if ($null -eq $function) {
    Write-Error 'Get-SplitLayoutDecision function not found'
    exit 1
}
. ([scriptblock]::Create($function.Extent.Text))
$cases = @(
    @{ Name = 'normal initial'; Height = 560; Splitter = 6; Current = -1; Upper = 250; Lower = 200; Distance = 250 },
    @{ Name = 'preserve valid'; Height = 560; Splitter = 6; Current = 310; Upper = 250; Lower = 200; Distance = 310 },
    @{ Name = 'clamp low'; Height = 560; Splitter = 6; Current = 20; Upper = 250; Lower = 200; Distance = 250 },
    @{ Name = 'clamp high'; Height = 560; Splitter = 6; Current = 500; Upper = 250; Lower = 200; Distance = 354 },
    @{ Name = 'constrained'; Height = 306; Splitter = 6; Current = -1; Upper = 166; Lower = 134; Distance = 166 },
    @{ Name = 'tiny'; Height = 7; Splitter = 6; Current = -1; Upper = 0; Lower = 1; Distance = 0 },
    @{ Name = 'zero'; Height = 0; Splitter = 6; Current = -1; Upper = 0; Lower = 0; Distance = 0 },
    @{ Name = 'negative'; Height = -20; Splitter = 6; Current = -1; Upper = 0; Lower = 0; Distance = 0 }
)
foreach ($case in $cases) {
    $got = Get-SplitLayoutDecision -AvailableHeight $case.Height -SplitterWidth $case.Splitter -CurrentDistance $case.Current
    if ($got.UpperMinimum -ne $case.Upper -or $got.LowerMinimum -ne $case.Lower -or $got.Distance -ne $case.Distance) {
        Write-Error ("{0}: got {1}/{2}/{3}, want {4}/{5}/{6}" -f $case.Name, $got.UpperMinimum, $got.LowerMinimum, $got.Distance, $case.Upper, $case.Lower, $case.Distance)
        exit 1
    }
    $usable = [Math]::Max(0, $case.Height - [Math]::Max(0, $case.Splitter))
    if (($got.UpperMinimum + $got.LowerMinimum) -gt $usable) {
        Write-Error ($case.Name + ': minimum sizes exceed usable height')
        exit 1
    }
    if ($got.Distance -lt $got.UpperMinimum -or $got.Distance -gt ($usable - $got.LowerMinimum)) {
        Write-Error ($case.Name + ': splitter distance is outside valid bounds')
        exit 1
    }
}
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell split-layout decision check failed: %v\n%s", err, output)
	}
}

func TestWindowsUIWelcomeDoesNotMutateSplitterState(t *testing.T) {
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
	functionStart := strings.Index(contents, "function Set-ResponsiveSplitLayout")
	if functionStart < 0 {
		t.Fatal("Set-ResponsiveSplitLayout function not found")
	}
	functionBody := contents[functionStart:]
	guard := `if ($script:activePage -eq "Welcome") { return }`
	guardIndex := strings.Index(functionBody, guard)
	clientSizeIndex := strings.Index(functionBody, "$script:operationSplit.ClientSize.Height")
	if guardIndex < 0 || clientSizeIndex < 0 || guardIndex > clientSizeIndex {
		t.Fatalf("welcome page must return before reading or saving splitter layout state")
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
		`function Get-RoomMembers`,
		`Invoke-VpnCtl -Arguments @("room", "members") -SuppressStandardOutput -Quiet:$Automatic`,
		`$script:uiFlowState -in @("Hosting", "JoinedMember")`,
		`Get-MemberLogDecision`,
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

func readWindowsPackagingFile(t *testing.T, name string) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", name)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func TestInstallScriptWaitsForNamedPipeReadinessAfterServiceStart(t *testing.T) {
	contents := readWindowsPackagingFile(t, "install.ps1")
	start := strings.Index(contents, "Start-Service -Name $ServiceName")
	waitOffset := strings.Index(contents[start:], "Wait-NodeServiceReady")
	wait := -1
	if start >= 0 && waitOffset >= 0 {
		wait = start + waitOffset
	}
	success := strings.Index(contents, `Write-Host "IPv6Mesh installed`)
	if start < 0 || wait < 0 || success < 0 || wait < start || wait > success {
		t.Fatalf("readiness order start=%d wait=%d success=%d", start, wait, success)
	}
	for _, required := range []string{"vpnctl.exe", `@("status")`, "15", "ConvertFrom-Json", ".ok"} {
		if !strings.Contains(contents, required) {
			t.Errorf("readiness logic missing %q", required)
		}
	}
}

func TestWindowsUIFlowTransitions(t *testing.T) {
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
if ($parseErrors.Count -gt 0) { exit 1 }
$function = $ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Test-UiFlowTransition' }, $true) | Select-Object -First 1
if ($null -eq $function) { Write-Error 'Test-UiFlowTransition function not found'; exit 1 }
. ([scriptblock]::Create($function.Extent.Text))
$allowed = @(
    @('Idle','HostSetup'), @('Idle','MemberSetup'),
    @('HostSetup','Idle'), @('HostSetup','PreparingHost'),
    @('MemberSetup','Idle'), @('MemberSetup','PreparingMember'),
    @('PreparingHost','Hosting'), @('PreparingHost','Cleaning'),
    @('PreparingMember','JoinedMember'), @('PreparingMember','Cleaning'),
    @('Hosting','Cleaning'), @('JoinedMember','Cleaning'),
    @('Cleaning','Idle'), @('Cleaning','HostSetup'), @('Cleaning','MemberSetup')
)
foreach ($pair in $allowed) { if (!(Test-UiFlowTransition -From $pair[0] -To $pair[1])) { throw ('rejected ' + ($pair -join ' -> ')) } }
$rejected = @(
    @('Hosting','MemberSetup'), @('Hosting','JoinedMember'),
    @('JoinedMember','HostSetup'), @('JoinedMember','Hosting'),
    @('PreparingHost','PreparingMember'), @('PreparingMember','PreparingHost')
)
foreach ($pair in $rejected) { if (Test-UiFlowTransition -From $pair[0] -To $pair[1]) { throw ('accepted ' + ($pair -join ' -> ')) } }
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell flow-transition check failed: %v\n%s", err, output)
	}
}

func TestWindowsUISingleInstance(t *testing.T) {
	contents := readWindowsPackagingFile(t, "ui.ps1")
	for _, required := range []string{
		`Global\IPv6Mesh.WindowsUI`,
		"function Enter-UiInstance",
		"function Exit-UiInstance",
		"IPv6Mesh 已在运行。请使用现有窗口。",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("UI missing single-instance behavior %q", required)
		}
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "ui.ps1")
	quotedPath := strings.ReplaceAll(uiPath, "'", "''")
	readyPath := filepath.Join(t.TempDir(), "ready")
	releasePath := filepath.Join(filepath.Dir(readyPath), "release")
	fileExists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	holder := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `$created = $false; $held = New-Object System.Threading.Mutex($true, 'Global\IPv6Mesh.WindowsUI', [ref]$created); if (!$created) { exit 2 }; [IO.File]::WriteAllText($env:IPV6MESH_MUTEX_READY, 'ready'); while (!(Test-Path -LiteralPath $env:IPV6MESH_MUTEX_RELEASE)) { Start-Sleep -Milliseconds 50 }; $held.ReleaseMutex(); $held.Dispose()`)
	holder.Env = append(os.Environ(), "IPV6MESH_MUTEX_READY="+readyPath, "IPV6MESH_MUTEX_RELEASE="+releasePath)
	if err := holder.Start(); err != nil {
		t.Fatalf("start mutex holder: %v", err)
	}
	defer func() {
		_ = os.WriteFile(releasePath, []byte("release"), 0600)
		if holder.ProcessState == nil {
			_ = holder.Process.Kill()
		}
		_ = holder.Wait()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !fileExists(readyPath) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !fileExists(readyPath) {
		t.Fatal("mutex holder did not become ready")
	}
	command := `
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { exit 1 }
$function = $ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Enter-UiInstance' }, $true) | Select-Object -First 1
. ([scriptblock]::Create($function.Extent.Text))
$script:uiMutex = $null
$script:ownsUiMutex = $false
if (Enter-UiInstance) { throw 'second instance acquired held mutex' }
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell single-instance check failed: %v\n%s", err, output)
	}
}

func TestWindowsUIMemberPreflight(t *testing.T) {
	contents := readWindowsPackagingFile(t, "ui.ps1")
	if !strings.Contains(contents, "function Assert-MemberControlReady") {
		t.Fatal("Assert-MemberControlReady function not found")
	}
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
if ($parseErrors.Count -gt 0) { exit 1 }
function Get-FunctionText([string]$name) {
    $node = $ast.FindAll({ param($candidate) $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -eq $name }, $true) | Select-Object -First 1
    if ($null -eq $node) { throw ($name + ' function not found') }
    return $node.Extent.Text
}
$preflight = Get-FunctionText 'Assert-MemberControlReady'
if ($preflight -notmatch 'Test-ControlHealth\s+-Quiet') { throw 'preflight is not quiet health check' }
if ($preflight -notmatch '房主控制面不可访问，请确认房主窗口仍在运行且 TCP 8080 可达。') { throw 'preflight message is not safe' }
if ($preflight -match 'Start-Sleep|while\s*\(|for\s*\(') { throw 'preflight contains a retry loop' }
$member = Get-FunctionText 'Join-MemberRoom'
$memberState = $member.IndexOf("Set-UiFlowState 'PreparingMember'")
$endpoint = $member.IndexOf('room", "endpoint')
$preflightCall = $member.IndexOf('Assert-MemberControlReady')
$install = $member.IndexOf('Install-NodeService')
if ($member.IndexOf('HostSetup') -ge 0 -or $memberState -lt 0 -or $memberState -gt $endpoint -or $endpoint -gt $preflightCall -or $preflightCall -gt $install) { throw 'member state/preflight ordering is invalid' }
	$hostText = Get-FunctionText 'Start-HostRoom'
	$hostState = $hostText.IndexOf("Set-UiFlowState 'PreparingHost'")
	if ($hostText.IndexOf('HostSetup') -lt 0 -or $hostState -lt 0 -or $hostState -gt $hostText.IndexOf('Refresh-LocalIPv6') -or $hostState -gt $hostText.IndexOf('Invoke-VpnCtl')) { throw 'host state ordering is invalid' }
`
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$scriptPath = '"+quotedPath+"';"+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell member-preflight check failed: %v\n%s", err, output)
	}
}
