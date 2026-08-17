# Persistent Diagnostics and Live Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Windows diagnostics area permanently visible on the create/join pages and refresh node status every two seconds without repetitive or sensitive log output.

**Architecture:** Keep the existing WinForms/PowerShell UI and shared bottom diagnostics group. Page navigation owns diagnostics visibility, while a single UI timer owns polling lifecycle; a pure status-transition helper decides when an automatic refresh deserves a log entry. Existing `vpnctl status` remains the source of truth, with a new quiet execution path used only by automatic polling.

**Tech Stack:** Go 1.24 test tooling, Windows PowerShell 5.1, System.Windows.Forms, existing `vpnctl` JSON status command, existing Windows installer builder.

---

## Execution Contract

- Base all work on local `main` commit `7e87b43` (`docs: design persistent live diagnostics`), which already includes upstream `origin/main` commit `41f4796`.
- Use an isolated worktree and branch named `codex/persistent-live-diagnostics`; do not implement in the planner's main checkout.
- Read `docs/superpowers/specs/2026-08-17-persistent-diagnostics-live-status-design.md` before editing.
- Follow strict RED → GREEN order. Capture the focused failing output before each implementation step.
- Do not change control-plane APIs, room enrollment, IPC protocol, VPN service behavior, or WireGuard data-plane code.
- Do not commit installers, `wireguard.dll`, generated payload archives, tokens, logs, or caches.
- Do not merge or push. Return the clean worktree path, branch, commit hashes, validation output, installer hash, and any unperformed manual checks to the planner for independent review.

## File Map

- Modify `packaging/windows/ui.ps1`: remove toggle controls; own page-visible diagnostics, timer lifecycle, safe quiet polling, refresh guards, and status-log deduplication.
- Modify `cmd/ipv6mesh-installer/main_windows_test.go`: add focused UI source/AST tests and direct tests of the pure PowerShell transition helper.
- Modify `README.md`: describe diagnostics as always visible on operation pages with live status.
- Modify `packaging/windows/README.md`: document the same Windows UI behavior for package users.

No new production file or dependency is needed.

### Task 1: Make diagnostics permanent on operation pages

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1:32-38,657-684,779-817`

- [ ] **Step 1: Add the failing page-lifecycle regression test**

Append this test to `cmd/ipv6mesh-installer/main_windows_test.go`:

```go
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
```

- [ ] **Step 2: Run the focused test and record RED**

Run:

```powershell
go test ./cmd/ipv6mesh-installer -run TestWindowsUIKeepsDiagnosticsVisibleOnOperationPages -count=1 -v
```

Expected: FAIL because `Toggle-Diagnostics` and both toggle buttons still exist and the page lifecycle does not call `BringToFront()`.

- [ ] **Step 3: Add page state and replace toggle behavior**

In the script-state block near `$script:diagnosticsPanel`, add:

```powershell
$script:activePage = "Welcome"
```

Replace `Show-Page` with:

```powershell
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
}
```

Delete the complete `Toggle-Diagnostics` function. Delete `$hostDiagnosticsButton` and `$memberDiagnosticsButton`, including their click handlers and `Controls.Add` calls. Do not move or remove the remaining diagnostics controls.

- [ ] **Step 4: Format and verify GREEN**

The PowerShell script must retain its UTF-8 BOM. Run:

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUIKeepsDiagnosticsVisibleOnOperationPages|TestWindowsPackageIncludesChineseUI|TestWindowsUIDoesNotDoubleQuoteSmartQuoteLogText' -count=1 -v
```

Expected: PASS for all three tests.

Parse the changed script:

```powershell
$tokens = $null
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path 'packaging/windows/ui.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { $errors | Format-List -Force; exit 1 }
```

Expected: exit 0 with no parse errors.

- [ ] **Step 5: Commit the permanent layout**

```powershell
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "fix: keep room diagnostics visible"
```

Expected: one commit containing only the test and UI layout change.

### Task 2: Define and test status-log transition decisions

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1:48-75,508-525`

- [ ] **Step 1: Add the failing direct PowerShell behavior test**

Append this test to `cmd/ipv6mesh-installer/main_windows_test.go`:

```go
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
```

- [ ] **Step 2: Run the focused test and record RED**

Run:

```powershell
go test ./cmd/ipv6mesh-installer -run TestWindowsUIStatusLogDecision -count=1 -v
```

Expected: FAIL with `Get-StatusLogDecision function not found`.

- [ ] **Step 3: Implement the pure transition helper**

Add this function after `Add-UiLog`:

```powershell
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
```

This helper must remain side-effect free. It receives only display-safe state and never receives status JSON, network ID, or credentials.

- [ ] **Step 4: Run focused and adjacent tests**

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUIStatusLogDecision|TestWindowsUIDoesNotDoubleQuoteSmartQuoteLogText|TestWindowsUIAcceptsMissingGlobalIPv6DuringStartup' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit the tested decision boundary**

```powershell
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "test: define live status log transitions"
```

Expected: one commit containing the direct behavior test and pure helper.

### Task 3: Add quiet automatic polling and timer lifecycle

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1:19-47,213-271,283-318,481-525,657-684,812-870`

- [ ] **Step 1: Add the failing live-polling integration guard test**

Append this test to `cmd/ipv6mesh-installer/main_windows_test.go`:

```go
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
```

- [ ] **Step 2: Run the focused test and record RED**

```powershell
go test ./cmd/ipv6mesh-installer -run TestWindowsUILiveStatusTimerUsesQuietDeduplicatedPolling -count=1 -v
```

Expected: FAIL because no status timer, quiet polling path, or lifecycle functions exist.

- [ ] **Step 3: Add status state and quiet command execution**

Add these script variables beside the existing UI state:

```powershell
$script:statusRefreshTimer = $null
$script:statusRefreshInProgress = $false
$script:hasStatusRefreshResult = $false
$script:lastStatusRefreshSucceeded = $false
$script:lastStatusFingerprint = ""
```

Add `[switch]$Quiet` to the parameter blocks of `Invoke-External`, `Invoke-VpnCtl`, and `Convert-ResultToJson`. Pass `Quiet = $Quiet` from `Invoke-VpnCtl` to `Invoke-External`.

In `Invoke-External`, guard all routine diagnostic writes with `if (!$Quiet)`: the start line, launch exception detail, stdout, stderr, and exit-code lines. Always rethrow a launch failure and always return the result object on a completed process; quiet mode changes logging only.

In `Convert-ResultToJson`, keep every existing stable-code mapping and safe thrown message, but guard its three `Add-UiLog` calls with `if (!$Quiet)`. Parsing and error semantics must be identical in quiet and normal modes.

The resulting calls used by automatic status must be exactly:

```powershell
$result = Invoke-VpnCtl -Arguments @("status") -SuppressStandardOutput -Quiet:$Automatic
$status = Convert-ResultToJson $result "读取节点状态" -Quiet:$Automatic
```

- [ ] **Step 4: Replace node status refresh with guarded, deduplicated behavior**

Replace `Get-NodeStatus` with:

```powershell
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
```

`network_id` remains internal only for connect/disconnect/leave calls. It must not be included in `$fingerprint`, labels, or logs.

- [ ] **Step 5: Add timer ownership and connect it to page navigation**

Add these functions immediately before `Show-Page`:

```powershell
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
```

At the end of `Show-Page`, after diagnostics visibility and z-order are set, add:

```powershell
    if ($Name -eq "Welcome") {
        Stop-StatusRefresh
    } else {
        Start-StatusRefresh
    }
```

After the diagnostics controls and their click handlers are created, initialize the timer:

```powershell
$script:statusRefreshTimer = New-Object System.Windows.Forms.Timer
$script:statusRefreshTimer.Interval = 2000
$script:statusRefreshTimer.Add_Tick({ Invoke-AutomaticStatusRefresh })
```

Change the manual refresh click handler to remain explicit:

```powershell
$refreshStatusButton.Add_Click({ $null = Get-NodeStatus })
```

At the start of `Stop-AllResources`, after setting `$script:cleanupStarted = $true` and before `Stop-StartedResources`, call:

```powershell
    Dispose-StatusRefreshTimer
```

Keep the existing `FormClosing` call to `Stop-AllResources`. This preserves one idempotent cleanup owner and guarantees the timer is disposed before processes and services stop.

- [ ] **Step 6: Run the live-polling tests to verify GREEN**

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUILiveStatusTimerUsesQuietDeduplicatedPolling|TestWindowsUIStatusLogDecision|TestWindowsUIKeepsDiagnosticsVisibleOnOperationPages' -count=1 -v
```

Expected: PASS.

Stress the focused behavior:

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUILiveStatusTimerUsesQuietDeduplicatedPolling|TestWindowsUIStatusLogDecision|TestWindowsUIKeepsDiagnosticsVisibleOnOperationPages|TestWindowsUIDoesNotDoubleQuoteSmartQuoteLogText' -count=20
```

Expected: PASS for 20 uncached runs.

- [ ] **Step 7: Parse, format, and commit live polling**

```powershell
gofmt -w cmd/ipv6mesh-installer/main_windows_test.go
$tokens = $null
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path 'packaging/windows/ui.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { $errors | Format-List -Force; exit 1 }
git diff --check
```

Expected: no parser errors, formatting output, or whitespace errors.

```powershell
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "feat: refresh room diagnostics live"
```

Expected: a clean commit containing the timer, quiet polling, status transition integration, and tests.

### Task 4: Update user documentation

**Files:**
- Modify: `README.md:18-36`
- Modify: `packaging/windows/README.md:20-43`

- [ ] **Step 1: Add a failing documentation assertion**

Extend `TestWindowsPackageIncludesChineseUI` in `cmd/ipv6mesh-installer/main_windows_test.go` by adding these required UI strings:

```go
		"诊断与日志",
		"节点状态读取已恢复",
```

Then add a new test:

```go
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
```

- [ ] **Step 2: Run the documentation test and record RED**

```powershell
go test ./cmd/ipv6mesh-installer -run TestWindowsDocumentationDescribesPersistentLiveDiagnostics -count=1 -v
```

Expected: FAIL because both documents still describe the diagnostics panel as troubleshooting-only and do not explain live refresh.

- [ ] **Step 3: Update both workflow descriptions**

In `README.md`, replace the current step 4 with this exact text:

```markdown
4. The create and join pages keep diagnostics and logs always visible. Node status
   refreshes every two seconds, while the log updates immediately and does not
   repeat unchanged status.
```

In `packaging/windows/README.md`, insert this paragraph after the two numbered workflow steps:

```markdown
The create and join pages keep diagnostics and logs always visible. Node status
refreshes every two seconds, while the log updates immediately and does not repeat
unchanged status. The welcome page hides diagnostics and pauses status refresh.
```

- [ ] **Step 4: Run documentation and UI regression tests**

```powershell
gofmt -w cmd/ipv6mesh-installer/main_windows_test.go
go test ./cmd/ipv6mesh-installer -run 'TestWindowsDocumentationDescribesPersistentLiveDiagnostics|TestWindowsPackageIncludesChineseUI|TestWindowsUIUsesRoomWorkflowAndActionableHealthCheck' -count=1 -v
git diff --check
```

Expected: tests pass and `git diff --check` is silent.

- [ ] **Step 5: Commit documentation**

```powershell
git add -- README.md packaging/windows/README.md cmd/ipv6mesh-installer/main_windows_test.go
git commit -m "docs: describe live room diagnostics"
```

Expected: documentation and its regression assertion are committed together.

### Task 5: Full verification and Windows installer rebuild

**Files:**
- Verify only: all tracked files
- Build output: `packaging/windows/dist/ipv6mesh-installer.exe` (ignored; do not add)

- [ ] **Step 1: Run Go formatting and whitespace checks**

```powershell
$goFiles = rg --files -g '*.go'
$formatDrift = @($goFiles | ForEach-Object { & 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe' -l $_ })
if ($formatDrift.Count -ne 0) { $formatDrift; exit 1 }
git diff --check
```

Expected: no output and exit 0.

- [ ] **Step 2: Parse every Windows PowerShell script**

```powershell
$failures = 0
Get-ChildItem -LiteralPath 'packaging/windows' -Filter '*.ps1' -File | ForEach-Object {
    $tokens = $null
    $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$tokens, [ref]$errors)
    if ($errors.Count -ne 0) {
        $failures++
        Write-Host "PARSE FAILURE: $($_.FullName)"
        $errors | Format-List -Force
    }
}
Write-Host "PS_PARSE_FAILURES=$failures"
if ($failures -ne 0) { exit 1 }
```

Expected: `PS_PARSE_FAILURES=0`.

- [ ] **Step 3: Run complete Go tests and vet**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test -count=1 ./...
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' vet ./...
```

Expected: every package passes; vet exits 0.

- [ ] **Step 4: Verify Windows compilation**

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
try {
    & 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test -run '^$' ./...
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
}
```

Expected: all Windows-target packages compile successfully.

- [ ] **Step 5: Verify WireGuard inputs before building**

Use these existing verified inputs:

```powershell
$wireGuardDll = 'C:\Users\Eser\Documents\Codex\2026-08-11\en\work\wireguard-nt-1.1\wireguard-nt\bin\amd64\wireguard.dll'
$wireGuardLicense = 'C:\Users\Eser\Documents\Codex\2026-08-11\en\work\wireguard-nt-1.1\wireguard-nt\LICENSE.txt'
if (!(Test-Path -LiteralPath $wireGuardDll -PathType Leaf)) { throw 'wireguard.dll missing' }
if (!(Test-Path -LiteralPath $wireGuardLicense -PathType Leaf)) { throw 'WireGuard license missing' }
$dllHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $wireGuardDll).Hash
if ($dllHash -ne 'B1B85E072C45D81358BE29D94C599DC76652F912BE8C0F0A41E2D5D89A6461D3') {
    throw "unexpected wireguard.dll hash: $dllHash"
}
```

Expected: both files exist and the DLL hash matches exactly.

- [ ] **Step 6: Rebuild and hash the installer**

```powershell
& '.\packaging\windows\build-installer.ps1' `
    -WireGuardDll $wireGuardDll `
    -WireGuardLicense $wireGuardLicense `
    -Version '0.1.0-dev' `
    -GoCommand 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe'
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$installer = 'packaging/windows/dist/ipv6mesh-installer.exe'
$installerInfo = Get-Item -LiteralPath $installer
$installerHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $installer).Hash
Write-Host "INSTALLER_SIZE=$($installerInfo.Length)"
Write-Host "INSTALLER_SHA256=$installerHash"
```

Expected: build exits 0 and prints a nonzero size and SHA-256.

- [ ] **Step 7: Confirm generated artifacts are ignored and the branch is clean**

```powershell
if (Test-Path -LiteralPath 'packaging/windows/payload.zip') { throw 'payload.zip was not cleaned' }
if (Test-Path -LiteralPath 'cmd/ipv6mesh-installer/payload_embed_windows.go') { throw 'payload_embed_windows.go was not cleaned' }
git status --short
git ls-files --error-unmatch 'packaging/windows/dist/ipv6mesh-installer.exe' 2>$null
if ($LASTEXITCODE -eq 0) { throw 'installer is tracked by Git' }
```

Expected: no temporary payload files, installer not tracked, and `git status --short` is empty.

- [ ] **Step 8: Perform available manual UI acceptance without overstating network coverage**

Launch the rebuilt installer on the available Windows desktop and check:

1. Welcome page has no diagnostics group.
2. Create page immediately shows diagnostics without a toggle button.
3. Join page immediately shows diagnostics without a toggle button.
4. Status visibly refreshes at about two-second intervals.
5. An unchanged status does not append the same line repeatedly.
6. Returning to welcome hides diagnostics and pauses refresh.
7. Closing produces no timer/disposed-control dialog.

If UAC, an interactive desktop, or a second public-IPv6 machine is unavailable, report the exact unperformed items. Do not claim real two-machine public-IPv6/WireGuard acceptance.

- [ ] **Step 9: Prepare the execution report for independent review**

Report:

- isolated worktree and branch;
- base and final commit hashes plus one-line subjects;
- focused RED evidence for Tasks 1-4;
- complete test, vet, parser, formatting, Windows compile, and build results;
- installer size and SHA-256;
- manual UI checks actually performed and those not performed;
- `git status --short` output;
- explicit confirmation that nothing was merged or pushed.

Do not make a final empty commit. Leave the verified branch clean for the planner's independent review.
