# Persistent Diagnostics and Live Status Design

## Goal

Fix the Windows UI behavior where clicking **显示诊断与日志** appears to do nothing, then replace that collapsible interaction with a diagnostics area that is always visible on the **创建网络** and **加入网络** pages and keeps node status current without flooding the log.

The welcome page remains focused on choosing a workflow and does not show diagnostics.

## Current Problem

`packaging/windows/ui.ps1` creates the host panel, member panel, and diagnostics group as sibling controls on the main form. The page panels overlap the diagnostics area. `Toggle-Diagnostics` changes `Visible`, but it does not move the diagnostics group in front of the active page panel, so the group can remain covered and the button appears ineffective. `Show-Page` also hides the diagnostics group on every page transition.

The log box already receives new UI log entries as they are emitted. Node status is different: `Get-NodeStatus` only runs after an explicit action or a manual click, and each call logs routine process-launch and status messages. Calling it unchanged on a timer would therefore create repetitive log noise.

## Approved User Experience

### Welcome page

- Show only the existing create-network and join-network choices.
- Keep the diagnostics area hidden.
- Stop automatic node-status refresh while this page is active.

### Create and join pages

- Show the diagnostics area permanently at the bottom of both pages.
- Ensure the diagnostics area is brought in front of the active page panel so it cannot be hidden by sibling-control z-order.
- Remove both **显示诊断与日志** buttons and remove `Toggle-Diagnostics`.
- Preserve the existing status label and the **刷新状态**, **连接**, **断开**, **离开房间**, **清空日志**, **复制日志**, and **导出日志** controls.
- Start live status refresh when either operation page becomes active. Refresh immediately once, then approximately every two seconds.

The current form dimensions and bottom diagnostics layout remain in scope. A right-side split layout and a separate diagnostics window are explicitly out of scope.

## Refresh Lifecycle

Create one `System.Windows.Forms.Timer` owned by the UI with a 2,000 millisecond interval.

- Entering the host or member page shows and fronts the diagnostics group, performs an immediate automatic refresh, and starts the timer.
- Returning to the welcome page stops the timer and hides the diagnostics group.
- Closing the form stops and disposes the timer before the existing resource cleanup proceeds.
- A timer tick is ignored when the primary create/join flow is busy, cleanup has started, the diagnostics page is no longer active, or a status refresh is already in progress.
- Manual refresh remains available. It uses the same refresh guard so it cannot overlap an automatic refresh.

The WinForms timer runs on the UI message loop and therefore does not create concurrent control updates. The explicit refresh-in-progress guard still protects against nested event handling and future changes.

## Status and Log Data Flow

`Get-NodeStatus` gains an automatic/quiet mode while keeping the manual behavior available.

1. Invoke `vpnctl status` with standard output suppressed from the general log, as it is today.
2. Automatic mode also suppresses routine process lifecycle lines such as **开始执行 vpnctl**.
3. Parse the existing JSON result and update the visible node-status label with only virtual IPv4, path state, and the stable error code.
4. Build a non-secret status fingerprint from those same display-safe fields.
5. Append a status log entry only when that fingerprint changes.

The UI stores only the last display-safe fingerprint and whether the last automatic refresh failed:

- The first automatic failure records one safe error line and updates the status label.
- Repeated identical failures do not append more lines.
- The first successful refresh after a failure records one recovery line.
- A later status change records the new display-safe summary once.
- A manual refresh always provides explicit user feedback even if the state is unchanged.

No raw status JSON, network ID, administrator token, invite token, bearer session, or private key may be written to the UI, logs, tests, exported diagnostics, or error dialogs. Unknown process and parsing errors retain the existing safe generic wording.

## Component Boundaries

The implementation is limited to:

- `packaging/windows/ui.ps1` for layout, page lifecycle, timer ownership, quiet command execution, refresh guarding, and log deduplication;
- `cmd/ipv6mesh-installer/main_windows_test.go` for Windows UI regression coverage;
- user-facing Windows packaging documentation only if its diagnostics description is now inaccurate.

Control-plane APIs, room enrollment, IPC protocol, VPN service behavior, and WireGuard data-plane code do not change.

## Error Handling

- Failure to start or execute `vpnctl status` must not terminate the UI timer or the application.
- Automatic polling reports a safe first failure and then remains quiet until state changes or recovery occurs.
- Manual refresh reports failure immediately.
- Timer callbacks must not access disposed controls during shutdown.
- Cleanup remains idempotent and stops live refresh before stopping resources.
- Existing secret redaction remains mandatory for all new log paths.

## TDD Strategy

Add failing Windows installer/UI tests before changing the script. The tests must establish that:

1. Both diagnostic-toggle buttons and `Toggle-Diagnostics` are absent.
2. The welcome page hides diagnostics and stops refresh.
3. The host and member pages show diagnostics, bring it to the front, refresh immediately, and start the timer.
4. The timer interval is 2,000 milliseconds and its tick requests an automatic status refresh.
5. Shutdown stops and disposes the timer before resource cleanup finishes.
6. Refresh-in-progress, primary-busy, cleanup, and inactive-page guards exist.
7. Automatic command execution has a quiet path that avoids routine lifecycle log lines.
8. Status-change, repeated-status, first-failure, repeated-failure, and recovery branches are represented and use display-safe fields only.
9. Existing smart-quote PowerShell AST regression coverage and secret-text prohibitions continue to pass.

Prefer behavior-oriented source/AST assertions consistent with the existing installer test suite. If extracting a small pure decision helper makes the status-log deduplication directly testable without launching WinForms, do so; do not introduce a new UI framework or testing dependency.

## Verification

The implementation is complete only after fresh evidence for all of the following:

- focused RED then GREEN tests for the new diagnostics behavior;
- `go test -count=1 ./...`;
- `go vet ./...`;
- PowerShell parser validation for every `packaging/windows/*.ps1` file;
- `GOOS=windows GOARCH=amd64 go test -run '^$' ./...`;
- `gofmt -l` reports no Go formatting drift;
- `git diff --check` reports no whitespace errors;
- the Windows installer rebuilds successfully using the verified WireGuard DLL and license inputs;
- generated installer payload files are cleaned and are not committed.

Manual UI acceptance must confirm:

- diagnostics are absent on the welcome page;
- diagnostics are immediately visible on both operation pages without clicking a toggle;
- node status updates at roughly two-second intervals;
- unchanged state does not repeatedly append log lines;
- failures are shown safely once and recovery is visible;
- returning to the welcome page pauses refresh;
- closing the application produces no timer or disposed-control error.

Real two-machine public-IPv6 and WireGuard connectivity acceptance remains a separate environment-dependent check and must not be claimed unless actually performed.
