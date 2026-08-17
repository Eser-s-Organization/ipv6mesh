# Responsive Windows UI Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace overlapping fixed-position Windows room controls with a responsive, resizable layout that combines managed rows, a constrained draggable diagnostics splitter, and scrolling fallback.

**Architecture:** Keep the existing Windows PowerShell 5.1 WinForms application and its room/status logic. A fill-docked root table owns the header and active content; operation pages share a horizontal split container whose upper panel scrolls and whose lower panel owns diagnostics. A pure splitter-policy function and a noninteractive real-control audit make size constraints and non-overlap behavior directly testable.

**Tech Stack:** Go 1.24 tests, Windows PowerShell 5.1, `System.Windows.Forms`, `System.Drawing`, existing Windows installer builder and WireGuard runtime inputs.

---

## Execution Contract

- Base the isolated worktree on local `main` commit `afd2ea3` (`docs: design responsive Windows UI layout`).
- Create branch `codex/responsive-windows-ui-layout`; do not implement in the planner's main checkout.
- Read `docs/superpowers/specs/2026-08-17-responsive-windows-ui-layout-design.md` and `docs/superpowers/specs/2026-08-17-persistent-diagnostics-live-status-design.md` before editing.
- Follow every RED → GREEN sequence and retain the focused failing output in the execution report.
- Preserve UTF-8 with BOM in `packaging/windows/ui.ps1`; Windows PowerShell 5.1 must continue parsing Chinese literals correctly.
- Do not change control-plane APIs, room enrollment, IPC, VPN service behavior, WireGuard behavior, two-second status timing, log fingerprinting, or secret handling.
- Do not commit installers, `wireguard.dll`, payload archives, generated embed files, logs, screenshots, or `.superpowers` visual-companion artifacts.
- Do not merge or push. Return the clean worktree, branch, commits, tests, manual checks, and installer hash to the planner for independent review.

## File Map

- Modify `packaging/windows/ui.ps1`: pure split sizing, responsive form sizing, managed layout hierarchy, page lifecycle, audit mode, and control naming used by the audit.
- Modify `cmd/ipv6mesh-installer/main_windows_test.go`: direct PowerShell policy tests, structural assertions, audit execution, and documentation regression tests.
- Modify `README.md`: describe resizable Windows UI, draggable diagnostics divider, and constrained-display scrolling.
- Modify `packaging/windows/README.md`: document the same packaged-UI behavior.

No new production dependency or separate production file is permitted.

### Task 1: Define the pure splitter sizing policy

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1` near the existing pure helper `Get-StatusLogDecision`

- [ ] **Step 1: Add a failing direct PowerShell sizing-policy test**

Append this test to `cmd/ipv6mesh-installer/main_windows_test.go`:

```go
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
    @{ Name = 'normal initial'; Height = 560; Splitter = 6; Current = -1; Upper = 250; Lower = 200; Distance = 249 },
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
```

- [ ] **Step 2: Run the focused test and record RED**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run TestWindowsUISplitLayoutDecision -count=1 -v
```

Expected: FAIL with `Get-SplitLayoutDecision function not found`.

- [ ] **Step 3: Implement the side-effect-free policy**

Add this function after `Get-StatusLogDecision` in `packaging/windows/ui.ps1`:

```powershell
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
```

- [ ] **Step 4: Verify GREEN and repeat the deterministic test**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe' -w cmd/ipv6mesh-installer/main_windows_test.go
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run TestWindowsUISplitLayoutDecision -count=20
```

Expected: PASS for 20 uncached runs.

- [ ] **Step 5: Commit the tested policy boundary**

```powershell
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "test: define responsive splitter policy"
```

### Task 2: Replace form-level fixed positioning with a managed root and split shell

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1` state block, `Show-Page`, UI construction, and form event wiring

- [ ] **Step 1: Replace the old visibility test with a failing managed-root regression test**

Replace `TestWindowsUIKeepsDiagnosticsVisibleOnOperationPages` with:

```go
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
```

- [ ] **Step 2: Run the new test and record RED**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run TestWindowsUIUsesManagedOperationShell -count=1 -v
```

Expected: FAIL because the fixed-position sibling controls are still present.

- [ ] **Step 3: Add responsive state and page-layout separation**

Add these state variables beside the existing page-control variables:

```powershell
$script:contentPanel = $null
$script:operationShell = $null
$script:operationSplit = $null
$script:updatingSplitLayout = $false
$script:userSplitterDistance = -1
```

Add these functions before `Show-Page`, and replace `Show-Page` with the version below:

```powershell
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
```

- [ ] **Step 4: Add responsive outer-window sizing**

Add this helper immediately before UI construction:

```powershell
function Set-ResponsiveWindowBounds {
    param([Parameter(Mandatory = $true)][System.Windows.Forms.Form]$Form)
    $workingArea = [System.Windows.Forms.Screen]::PrimaryScreen.WorkingArea
    $preferredOuter = $Form.SizeFromClientSize((New-Object System.Drawing.Size(1120, 720)))
    $minimumWidth = [Math]::Min(900, $workingArea.Width)
    $minimumHeight = [Math]::Min(640, $workingArea.Height)
    $Form.MinimumSize = New-Object System.Drawing.Size($minimumWidth, $minimumHeight)
    $Form.Size = New-Object System.Drawing.Size(
        ([Math]::Min($preferredOuter.Width, $workingArea.Width)),
        ([Math]::Min($preferredOuter.Height, $workingArea.Height))
    )
}
```

When creating the form, set:

```powershell
$script:form.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::Sizable
$script:form.AutoScaleMode = [System.Windows.Forms.AutoScaleMode]::Dpi
$script:form.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 9)
Set-ResponsiveWindowBounds $script:form
```

Delete the old fixed `ClientSize` and `MinimumSize` assignments.

- [ ] **Step 5: Build the root header, mutually exclusive views, and operation split**

Replace the form-level title/status/panel setup with this managed hierarchy. Keep the existing click handlers exactly as shown:

```powershell
$rootLayout = New-Object System.Windows.Forms.TableLayoutPanel
$rootLayout.Name = "RootLayout"
$rootLayout.Dock = [System.Windows.Forms.DockStyle]::Fill
$rootLayout.ColumnCount = 1
$rootLayout.RowCount = 2
$rootLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
$rootLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 100)))
$script:form.Controls.Add($rootLayout)

$headerLayout = New-Object System.Windows.Forms.TableLayoutPanel
$headerLayout.Name = "HeaderLayout"
$headerLayout.Dock = [System.Windows.Forms.DockStyle]::Fill
$headerLayout.AutoSize = $true
$headerLayout.Padding = New-Object System.Windows.Forms.Padding(20, 12, 20, 10)
$headerLayout.ColumnCount = 2
$headerLayout.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::AutoSize)))
$headerLayout.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))
$rootLayout.Controls.Add($headerLayout, 0, 0)

$title = New-Object System.Windows.Forms.Label
$title.Name = "ProductTitle"
$title.Text = "IPv6Mesh 远程组网"
$title.AutoSize = $true
$title.Anchor = [System.Windows.Forms.AnchorStyles]::Left
$title.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 15, [System.Drawing.FontStyle]::Bold)
$headerLayout.Controls.Add($title, 0, 0)

$script:statusLabel = New-Object System.Windows.Forms.Label
$script:statusLabel.Name = "HeaderStatus"
$script:statusLabel.Text = "等待选择"
$script:statusLabel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:statusLabel.AutoEllipsis = $true
$script:statusLabel.TextAlign = [System.Drawing.ContentAlignment]::MiddleRight
$headerLayout.Controls.Add($script:statusLabel, 1, 0)

$script:contentPanel = New-Object System.Windows.Forms.Panel
$script:contentPanel.Name = "ContentPanel"
$script:contentPanel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:contentPanel.Padding = New-Object System.Windows.Forms.Padding(20, 8, 20, 20)
$rootLayout.Controls.Add($script:contentPanel, 0, 1)

$script:welcomePanel = New-Object System.Windows.Forms.TableLayoutPanel
$script:welcomePanel.Name = "WelcomePanel"
$script:welcomePanel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:welcomePanel.ColumnCount = 4
$script:welcomePanel.RowCount = 5
$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
$script:welcomePanel.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 25)))
$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 35)))
$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Absolute, 100)))
$script:welcomePanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 65)))
$script:contentPanel.Controls.Add($script:welcomePanel)

$welcomeTitle = New-Object System.Windows.Forms.Label
$welcomeTitle.Name = "WelcomeTitle"
$welcomeTitle.Text = "你想做什么？"
$welcomeTitle.AutoSize = $true
$welcomeTitle.Anchor = [System.Windows.Forms.AnchorStyles]::None
$welcomeTitle.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 22)
$script:welcomePanel.Controls.Add($welcomeTitle, 1, 1)
$script:welcomePanel.SetColumnSpan($welcomeTitle, 2)

$welcomeHelp = New-Object System.Windows.Forms.Label
$welcomeHelp.Name = "WelcomeHelp"
$welcomeHelp.Text = "选择一种方式开始 IPv6Mesh 房间流程。"
$welcomeHelp.AutoSize = $true
$welcomeHelp.Anchor = [System.Windows.Forms.AnchorStyles]::None
$script:welcomePanel.Controls.Add($welcomeHelp, 1, 2)
$script:welcomePanel.SetColumnSpan($welcomeHelp, 2)

$createButton = New-Object System.Windows.Forms.Button
$createButton.Name = "WelcomeCreate"
$createButton.Text = "创建网络"
$createButton.Dock = [System.Windows.Forms.DockStyle]::Fill
$createButton.Margin = New-Object System.Windows.Forms.Padding(10, 15, 10, 15)
$createButton.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 12)
$createButton.Add_Click({ Show-HostPage })
$script:welcomePanel.Controls.Add($createButton, 1, 3)

$joinButton = New-Object System.Windows.Forms.Button
$joinButton.Name = "WelcomeJoin"
$joinButton.Text = "加入网络"
$joinButton.Dock = [System.Windows.Forms.DockStyle]::Fill
$joinButton.Margin = New-Object System.Windows.Forms.Padding(10, 15, 10, 15)
$joinButton.Font = New-Object System.Drawing.Font("Microsoft YaHei UI", 12)
$joinButton.Add_Click({ Show-MemberPage })
$script:welcomePanel.Controls.Add($joinButton, 2, 3)

$script:operationShell = New-Object System.Windows.Forms.Panel
$script:operationShell.Name = "OperationShell"
$script:operationShell.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:operationShell.Visible = $false
$script:contentPanel.Controls.Add($script:operationShell)

$script:operationSplit = New-Object System.Windows.Forms.SplitContainer
$script:operationSplit.Name = "OperationSplit"
$script:operationSplit.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:operationSplit.Orientation = [System.Windows.Forms.Orientation]::Horizontal
$script:operationSplit.FixedPanel = [System.Windows.Forms.FixedPanel]::Panel1
$script:operationSplit.SplitterWidth = 6
$script:operationSplit.Panel1.AutoScroll = $true
$script:operationShell.Controls.Add($script:operationSplit)
```

Change the existing parent assignments as follows while leaving their internal fixed layout for Tasks 3 and 4:

```powershell
$script:hostPanel.Location = New-Object System.Drawing.Point(0, 0)
$script:operationSplit.Panel1.Controls.Add($script:hostPanel)
$script:memberPanel.Location = New-Object System.Drawing.Point(0, 0)
$script:operationSplit.Panel1.Controls.Add($script:memberPanel)
$script:diagnosticsPanel.Location = New-Object System.Drawing.Point(0, 0)
$script:diagnosticsPanel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:diagnosticsPanel.Visible = $true
$script:operationSplit.Panel2.Controls.Add($script:diagnosticsPanel)
```

Delete the corresponding `form.Controls.Add` calls and the diagnostics `BringToFront` logic.

- [ ] **Step 6: Wire resizing and user splitter movement without network side effects**

After creating the split container, add:

```powershell
$script:operationSplit.Add_SplitterMoved({
    if (!$script:updatingSplitLayout) {
        $script:userSplitterDistance = $script:operationSplit.SplitterDistance
        Set-ResponsiveSplitLayout
    }
})
$script:operationSplit.Add_SizeChanged({ Set-ResponsiveSplitLayout })
$script:form.Add_Shown({ Set-ResponsiveSplitLayout })
```

These handlers must call only layout functions. Do not call `Get-NodeStatus`, `Start-StatusRefresh`, or any room/service function from them.

- [ ] **Step 7: Verify managed shell GREEN**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe' -w cmd/ipv6mesh-installer/main_windows_test.go
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run 'TestWindowsUIUsesManagedOperationShell|TestWindowsUILiveStatusTimerUsesQuietDeduplicatedPolling|TestWindowsUIStatusLogDecision' -count=1 -v
$tokens = $null
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path 'packaging/windows/ui.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { $errors | Format-List -Force; exit 1 }
```

Expected: all tests pass and the parser reports no errors.

- [ ] **Step 8: Commit the root layout**

```powershell
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "fix: separate operation and diagnostics layout"
```

### Task 3: Convert create and join pages to responsive grids

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1` control factories and host/member construction

- [ ] **Step 1: Add a failing operation-page layout test**

Append:

```go
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
		`$page.MinimumSize = New-Object System.Drawing.Size(820, 0)`,
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
```

- [ ] **Step 2: Run RED**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run TestWindowsUIOperationPagesUseResponsiveGrids -count=1 -v
```

Expected: FAIL because both operation pages still use fixed coordinates.

- [ ] **Step 3: Add managed-layout control factories**

Add these functions beside the existing temporary fixed-position factories:

```powershell
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
    $page.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Absolute, 130)))
    $page.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Percent, 100)))
    $page.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle([System.Windows.Forms.SizeType]::Absolute, 150)))
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
    $Page.Controls.Add($Control, $Column, $Row)
    if ($ColumnSpan -gt 1) { $Page.SetColumnSpan($Control, $ColumnSpan) }
}
```

- [ ] **Step 4: Replace the host page construction**

Delete the old host-panel block and insert:

```powershell
$script:hostPanel = New-ResponsivePageGrid "HostPanel"
$script:hostPanel.RowCount = 6
for ($row = 0; $row -lt 6; $row++) {
    $script:hostPanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
}
$script:operationSplit.Panel1.Controls.Add($script:hostPanel)

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
```

- [ ] **Step 5: Replace the member page construction**

Delete the old member-panel block and insert:

```powershell
$script:memberPanel = New-ResponsivePageGrid "MemberPanel"
$script:memberPanel.RowCount = 6
for ($row = 0; $row -lt 6; $row++) {
    $script:memberPanel.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
}
$script:memberPanel.Visible = $false
$script:operationSplit.Panel1.Controls.Add($script:memberPanel)

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
```

Keep the existing `$script:ipv6AddressBox.Add_TextChanged` handler after construction.

- [ ] **Step 6: Verify operation-page GREEN and commit**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe' -w cmd/ipv6mesh-installer/main_windows_test.go
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run 'TestWindowsUIOperationPagesUseResponsiveGrids|TestWindowsUIUsesManagedOperationShell|TestWindowsPackageIncludesChineseUI' -count=1 -v
git diff --check
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "feat: make room operation pages responsive"
```

Expected: tests pass, whitespace check is silent, and the commit contains only operation-page layout work.

### Task 4: Make diagnostics fill and wrap inside the lower split panel

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1` diagnostics construction and obsolete fixed-position factories

- [ ] **Step 1: Add a failing diagnostics-layout regression test**

Append:

```go
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
```

- [ ] **Step 2: Run RED**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run TestWindowsUIDiagnosticsUsesFillAndWrappingLayout -count=1 -v
```

Expected: FAIL because diagnostics and log controls still have fixed bounds.

- [ ] **Step 3: Replace diagnostics construction with the managed layout**

Delete the old diagnostics block and insert:

```powershell
$script:diagnosticsPanel = New-Object System.Windows.Forms.GroupBox
$script:diagnosticsPanel.Name = "DiagnosticsPanel"
$script:diagnosticsPanel.Text = "诊断与日志"
$script:diagnosticsPanel.Dock = [System.Windows.Forms.DockStyle]::Fill
$script:diagnosticsPanel.Visible = $true
$script:diagnosticsPanel.Padding = New-Object System.Windows.Forms.Padding(12, 8, 12, 12)
$script:operationSplit.Panel2.Controls.Add($script:diagnosticsPanel)

$diagnosticsLayout = New-Object System.Windows.Forms.TableLayoutPanel
$diagnosticsLayout.Name = "DiagnosticsLayout"
$diagnosticsLayout.Dock = [System.Windows.Forms.DockStyle]::Fill
$diagnosticsLayout.ColumnCount = 1
$diagnosticsLayout.RowCount = 4
$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::Percent, 100)))
$diagnosticsLayout.RowStyles.Add((New-Object System.Windows.Forms.RowStyle([System.Windows.Forms.SizeType]::AutoSize)))
$script:diagnosticsPanel.Controls.Add($diagnosticsLayout)

$script:nodeStatusLabel = New-LayoutLabel "NodeStatus" "节点服务未检查状态"
$script:nodeStatusLabel.AutoSize = $false
$script:nodeStatusLabel.MinimumSize = New-Object System.Drawing.Size(0, 28)
$script:nodeStatusLabel.AutoEllipsis = $true
$script:nodeStatusLabel.Dock = [System.Windows.Forms.DockStyle]::Fill
$diagnosticsLayout.Controls.Add($script:nodeStatusLabel, 0, 0)

$statusActions = New-Object System.Windows.Forms.FlowLayoutPanel
$statusActions.Name = "StatusActions"
$statusActions.Dock = [System.Windows.Forms.DockStyle]::Fill
$statusActions.AutoSize = $true
$statusActions.WrapContents = $true
$statusActions.FlowDirection = [System.Windows.Forms.FlowDirection]::LeftToRight
$diagnosticsLayout.Controls.Add($statusActions, 0, 1)

$refreshStatusButton = New-LayoutButton "RefreshStatus" "刷新状态" 100
$refreshStatusButton.Add_Click({ $null = Get-NodeStatus })
$statusActions.Controls.Add($refreshStatusButton)
$connectButton = New-LayoutButton "ConnectNode" "连接" 90
$connectButton.Add_Click({ Connect-Node })
$statusActions.Controls.Add($connectButton)
$disconnectButton = New-LayoutButton "DisconnectNode" "断开" 90
$disconnectButton.Add_Click({ Disconnect-Node })
$statusActions.Controls.Add($disconnectButton)
$leaveButton = New-LayoutButton "LeaveRoom" "离开房间" 110
$leaveButton.Add_Click({ Leave-Node })
$statusActions.Controls.Add($leaveButton)

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
$diagnosticsLayout.Controls.Add($script:logBox, 0, 2)

$logActions = New-Object System.Windows.Forms.FlowLayoutPanel
$logActions.Name = "LogActions"
$logActions.Dock = [System.Windows.Forms.DockStyle]::Fill
$logActions.AutoSize = $true
$logActions.WrapContents = $true
$logActions.FlowDirection = [System.Windows.Forms.FlowDirection]::LeftToRight
$diagnosticsLayout.Controls.Add($logActions, 0, 3)

$clearLogButton = New-LayoutButton "ClearLog" "清空日志" 100
$clearLogButton.Add_Click({ $script:logLines.Clear(); $script:logBox.Clear() })
$logActions.Controls.Add($clearLogButton)
$copyLogButton = New-LayoutButton "CopyLog" "复制日志" 100
$copyLogButton.Add_Click({ [System.Windows.Forms.Clipboard]::SetText(($script:logLines -join [Environment]::NewLine)) })
$logActions.Controls.Add($copyLogButton)
$exportLogButton = New-LayoutButton "ExportLog" "导出日志" 100
$exportLogButton.Add_Click({ Export-UiLog })
$logActions.Controls.Add($exportLogButton)
```

Delete obsolete `New-Label`, `New-TextBox`, and `New-Button` after confirming no call sites remain:

```powershell
rg -n 'New-Label|New-TextBox|New-Button' packaging/windows/ui.ps1
```

Expected before deletion: definitions only. Expected after deletion: no matches.

- [ ] **Step 4: Verify diagnostics GREEN and stress layout-source regressions**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe' -w cmd/ipv6mesh-installer/main_windows_test.go
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run 'TestWindowsUIDiagnosticsUsesFillAndWrappingLayout|TestWindowsUIOperationPagesUseResponsiveGrids|TestWindowsUIUsesManagedOperationShell|TestWindowsUILiveStatusTimerUsesQuietDeduplicatedPolling' -count=20
$tokens = $null
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path 'packaging/windows/ui.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { $errors | Format-List -Force; exit 1 }
git diff --check
```

Expected: 20 passes, zero parser errors, and no whitespace errors.

- [ ] **Step 5: Commit responsive diagnostics**

```powershell
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "feat: make room diagnostics resizable"
```

### Task 5: Add a noninteractive audit of the real WinForms control tree

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `packaging/windows/ui.ps1` parameter list, audit helpers, and pre-run branch

- [ ] **Step 1: Add the failing audit execution test**

Append:

```go
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
	for _, required := range []string{`"Case":"preferred"`, `"Case":"minimum"`, `"Case":"large"`, `"Case":"constrained"`, `"Case":"large-font"`, `"Case":"upper-limit"`, `"Case":"lower-limit"`} {
		if !strings.Contains(compact, required) {
			t.Errorf("responsive WinForms layout audit missing sample %s", required)
		}
	}
}
```

- [ ] **Step 2: Run RED**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run TestWindowsUIResponsiveLayoutAudit -count=1 -v
```

Expected: FAIL because `ui.ps1` does not recognize `-LayoutAudit`.

- [ ] **Step 3: Add the audit parameter and geometry helpers**

Add `[switch]$LayoutAudit` after the existing `Version` parameter, including the required comma before it:

```powershell
    [string]$Version = "dev",
    [switch]$LayoutAudit
```

Add these helpers after the layout factories:

```powershell
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
```

- [ ] **Step 4: Implement the real-control layout audit**

Add this function after the helpers from Step 3:

```powershell
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
                    if (!$leafIntersection.IsEmpty) {
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
        Errors = @($errors)
        Samples = @($samples)
    }
}
```

- [ ] **Step 5: Add the audit-only exit before logging, page activation, and the message loop**

After timer construction and all control event handlers are attached, but before `Add-UiLog`, `Show-WelcomePage`, or `Application.Run`, insert:

```powershell
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
```

This branch must precede `Show-WelcomePage` so audit execution never starts the immediate automatic refresh.

- [ ] **Step 6: Verify audit GREEN, parser safety, and repetition**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe' -w cmd/ipv6mesh-installer/main_windows_test.go
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run 'TestWindowsUIResponsiveLayoutAudit|TestWindowsUISplitLayoutDecision|TestWindowsUIDiagnosticsUsesFillAndWrappingLayout|TestWindowsUIOperationPagesUseResponsiveGrids|TestWindowsUIUsesManagedOperationShell' -count=1 -v
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run 'TestWindowsUIResponsiveLayoutAudit|TestWindowsUISplitLayoutDecision' -count=20
$tokens = $null
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path 'packaging/windows/ui.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { $errors | Format-List -Force; exit 1 }
```

Expected: audit and policy tests pass 20 times; parser reports zero errors.

- [ ] **Step 7: Commit executable layout acceptance**

```powershell
git add -- cmd/ipv6mesh-installer/main_windows_test.go packaging/windows/ui.ps1
git commit -m "test: audit responsive room layout"
```

### Task 6: Document, build, and manually verify the responsive window

**Files:**
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go`
- Modify: `README.md`
- Modify: `packaging/windows/README.md`
- Verify: all tracked files
- Build only: ignored files under `packaging/windows/dist/`

- [ ] **Step 1: Extend the documentation test and record RED**

In `TestWindowsDocumentationDescribesPersistentLiveDiagnostics`, change the required phrase list to:

```go
		for _, required := range []string{
			"always visible",
			"every two seconds",
			"does not repeat unchanged status",
			"window can be resized",
			"drag the horizontal diagnostics divider",
			"operation area scrolls",
		} {
```

Run:

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run TestWindowsDocumentationDescribesPersistentLiveDiagnostics -count=1 -v
```

Expected: FAIL because neither document describes the new layout behavior.

- [ ] **Step 2: Add the approved behavior to both documents**

Immediately after the existing persistent-diagnostics paragraph in both `README.md` and `packaging/windows/README.md`, add:

```markdown
The Windows room window can be resized. On the create and join pages, drag the
horizontal diagnostics divider to allocate more space to room settings or logs.
On a constrained display or with larger system text, the operation area scrolls
instead of covering diagnostics or other controls.
```

- [ ] **Step 3: Verify documentation GREEN and commit**

```powershell
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe' -w cmd/ipv6mesh-installer/main_windows_test.go
& 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test ./cmd/ipv6mesh-installer -run 'TestWindowsDocumentationDescribesPersistentLiveDiagnostics|TestWindowsUIResponsiveLayoutAudit|TestWindowsPackageIncludesChineseUI' -count=1 -v
git diff --check
git add -- README.md packaging/windows/README.md cmd/ipv6mesh-installer/main_windows_test.go
git commit -m "docs: describe responsive room window"
```

Expected: tests pass, whitespace check is silent, and the documentation commit is cleanly separated.

- [ ] **Step 4: Run formatting, whitespace, and PowerShell parser gates**

```powershell
$goFormat = 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\gofmt.exe'
$goFiles = rg --files -g '*.go'
$formatDrift = @($goFiles | ForEach-Object { & $goFormat -l $_ })
if ($formatDrift.Count -ne 0) { $formatDrift; exit 1 }
git diff --check

$parseFailures = 0
Get-ChildItem -LiteralPath 'packaging/windows' -Filter '*.ps1' -File | ForEach-Object {
    $tokens = $null
    $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$tokens, [ref]$errors)
    if ($errors.Count -ne 0) {
        $parseFailures++
        Write-Host "PARSE FAILURE: $($_.FullName)"
        $errors | Format-List -Force
    }
}
Write-Host "PS_PARSE_FAILURES=$parseFailures"
if ($parseFailures -ne 0) { exit 1 }
```

Expected: no Go formatting drift, no whitespace error, and `PS_PARSE_FAILURES=0`.

- [ ] **Step 5: Run focused tests repeatedly, then the complete Go suite and vet**

```powershell
$go = 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe'
& $go test ./cmd/ipv6mesh-installer -run 'TestWindowsUIResponsiveLayoutAudit|TestWindowsUISplitLayoutDecision|TestWindowsUIDiagnosticsUsesFillAndWrappingLayout|TestWindowsUIOperationPagesUseResponsiveGrids|TestWindowsUIUsesManagedOperationShell|TestWindowsUILiveStatusTimerUsesQuietDeduplicatedPolling|TestWindowsUIStatusLogDecision' -count=20
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& $go test -count=1 ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& $go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
```

Expected: all focused tests pass 20 times; every package passes; vet exits 0.

- [ ] **Step 6: Verify Windows amd64 compilation**

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
try {
    & 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe' test -run '^$' ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
}
```

Expected: every Windows-target package compiles.

- [ ] **Step 7: Verify the existing WireGuard inputs**

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

Expected: inputs exist and the DLL hash matches exactly.

- [ ] **Step 8: Build a launchable package and perform real WinForms acceptance**

Build an ignored package directory:

```powershell
$distDirectory = 'packaging/windows/dist'
if (!(Test-Path -LiteralPath $distDirectory -PathType Container)) {
    New-Item -ItemType Directory -Path $distDirectory | Out-Null
}
$uiPackage = Join-Path (Resolve-Path $distDirectory).Path 'responsive-ui-audit'
& '.\packaging\windows\build.ps1' `
    -OutputDirectory $uiPackage `
    -WireGuardDll $wireGuardDll `
    -WireGuardLicense $wireGuardLicense `
    -Version '0.1.0-dev' `
    -GoCommand 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe'
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File (Join-Path $uiPackage 'ui.ps1') -PackageDirectory $uiPackage -Version '0.1.0-dev'
```

On the real window, record results for every item:

1. Welcome at initial size: centered choices, no diagnostics.
2. Host at initial size: create button fully visible above diagnostics.
3. Member at initial size: join button fully visible above diagnostics.
4. Shrink to the permitted minimum: no overlap; upper area scrolls if required.
5. Enlarge the window: host/member input and log widths grow; log height grows.
6. Drag the splitter to both limits: it clamps and neither panel disappears.
7. Choose a middle splitter position, switch Host → Welcome → Member: the position remains for the process.
8. Leave an operation page open long enough for several timer ticks: status refresh remains about two seconds and unchanged failures/status do not flood the log.
9. Close the form: no disposed-control, splitter, timer, or cleanup error dialog.

If Windows display scaling cannot be changed safely in the current environment, rely on the automated large-font audit and explicitly report that the interactive DPI change was not performed. Do not claim real two-machine IPv6/WireGuard acceptance.

- [ ] **Step 9: Rebuild and hash the installer**

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

Expected: installer build exits 0 and prints a nonzero size and SHA-256.

- [ ] **Step 10: Confirm build cleanup, secret safety, and clean Git state**

```powershell
if (Test-Path -LiteralPath 'packaging/windows/payload.zip') { throw 'payload.zip was not cleaned' }
if (Test-Path -LiteralPath 'cmd/ipv6mesh-installer/payload_embed_windows.go') { throw 'payload_embed_windows.go was not cleaned' }
git ls-files --error-unmatch 'packaging/windows/dist/ipv6mesh-installer.exe' 2>$null
if ($LASTEXITCODE -eq 0) { throw 'installer is tracked by Git' }

$suspicious = git diff afd2ea3...HEAD | rg '(Bearer [A-Za-z0-9._-]{24,}|BEGIN (RSA |OPENSSH )?PRIVATE KEY)'
if ($LASTEXITCODE -eq 0) { $suspicious; throw 'possible committed secret material found' }
git status --short
```

Expected: generated payload files are absent, installer is untracked, secret scan has no match, and `git status --short` is empty.

- [ ] **Step 11: Prepare the execution report for independent review**

Report all of the following without merging or pushing:

- worktree, branch, base commit, final commit, and every task commit with subject;
- focused RED evidence for Tasks 1 through 6;
- focused `-count=20`, full test, vet, parser, formatting, whitespace, and Windows compile results;
- headless audit cases and real WinForms checks actually performed;
- installer size and SHA-256 plus verified WireGuard DLL hash;
- any unavailable interactive DPI or real two-machine checks;
- confirmation that generated payloads are absent and installer is not tracked;
- final `git status --short` output.

Do not make an empty final commit. Leave the branch clean for the planner's independent code, runtime, and GitHub integration review.
