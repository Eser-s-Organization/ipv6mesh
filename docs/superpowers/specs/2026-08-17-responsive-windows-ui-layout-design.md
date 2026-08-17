# Responsive Windows UI Layout Design

## Goal

Eliminate every control overlap in the Windows room UI and make the window resize cleanly. The create-network and join-network pages must keep the persistent live diagnostics area introduced in `v0.1.0-debug.16`, while allowing the user to adjust the vertical balance between the operation area and diagnostics.

The approved behavior combines two safeguards:

- use a safe minimum window size on displays that can accommodate it;
- use scrolling when screen size, DPI scaling, or enlarged system fonts leave less usable space than the safe design size.

This specification supersedes only the fixed-size and fixed-position layout statements in `2026-08-17-persistent-diagnostics-live-status-design.md`. That earlier specification remains authoritative for diagnostics visibility, timer lifecycle, two-second refresh, log deduplication, and secret safety.

## Current Problem

`packaging/windows/ui.ps1` constructs the main form, page panels, and diagnostics group with fixed pixel coordinates. The page panels are positioned at `(20, 70)`, while the diagnostics group is a sibling control positioned directly on the form at `(40, 350)`.

The create and join action buttons are positioned at page-relative `Y = 275` with height `44`. Their form-relative vertical range is therefore `345..389`. The diagnostics group begins at form-relative `Y = 350`, so the controls overlap by 39 pixels. Bringing diagnostics to the front only changes which control covers the other; it cannot correct the intersecting geometry.

The form also fixes both its client size and minimum size to `1120 x 720`. The page panels, input fields, buttons, diagnostics group, and log box do not respond to a larger client area, while a smaller work area or larger effective font can clip content.

## Approved User Experience

### Welcome page

- Keep the existing create-network and join-network choices.
- Fill the available content area and keep the choices centered as the window grows.
- Do not show diagnostics.
- Preserve the existing behavior that pauses automatic node-status refresh.

### Create and join pages

- Present an upper operation area and a lower diagnostics area separated by a horizontal draggable splitter.
- Never allow controls from one area to cover controls in the other area.
- Keep diagnostics visible for the full time either operation page is active.
- Preserve all current room actions, status labels, diagnostics actions, log actions, live two-second status refresh, and log deduplication behavior.
- Allow the user to drag the splitter to allocate more height to either area, subject to safe upper and lower bounds.
- Keep the user's splitter choice during the current process and across page switches. Do not persist it across application launches.

### Window resizing

- Keep the initial preferred client size at `1120 x 720` when the current screen work area can accommodate it.
- Use `900 x 640` as the nominal safe minimum outer window size on displays that can accommodate it.
- Never set the actual initial size or minimum size larger than the current screen working area. The screen working area is authoritative on smaller displays.
- Allow free enlargement. Input fields, page width, diagnostics width, and the log box must consume newly available space.
- When available space is below the nominal layout requirement because of a small work area, DPI scaling, or enlarged fonts, keep areas structurally separate and enable scrolling in the upper operation area.
- Do not persist window size or position across launches.

## Layout Architecture

### Root form

The form remains a standard sizable WinForms window. Set an explicit DPI-aware autoscaling mode consistent with WinForms on supported Windows versions.

Replace direct placement of major panels on the form with a root `TableLayoutPanel` docked to `Fill`:

1. an auto-sized header row containing the product title and current status;
2. a content row using all remaining height.

The header uses a stretchable status column so the status text remains right-aligned without colliding with the product title.

### Content views

The content row owns two mutually exclusive views:

- the welcome view;
- one shared operation shell.

The operation shell owns a horizontal `SplitContainer` docked to `Fill`. Its first panel is the operation-page viewport. Its second panel owns the one shared diagnostics component. The diagnostics component must no longer be a form-level sibling of the page panels.

`Show-Page` switches the upper operation content between the host and member pages. Returning to the welcome page hides the complete operation shell. This makes the diagnostics lifecycle a consequence of the active view rather than z-order manipulation.

### Operation area

The first split panel enables `AutoScroll`. Host and member content uses managed layout containers rather than form-relative absolute positioning:

- a table layout provides label, stretchable value, and action columns;
- the value column takes remaining horizontal space;
- controls span columns when their content requires it;
- the primary create/join button occupies its own row below the virtual IPv4 status;
- margins and row styles provide spacing instead of overlapping coordinate ranges.

The page's preferred size becomes the scrollable virtual extent. At normal sizes every control is visible without scrolling. At constrained sizes the operation area scrolls; controls do not move over diagnostics.

### Diagnostics area

The second split panel owns a `GroupBox` docked to `Fill`. Inside it, a table layout contains:

1. the node-status label;
2. a flow layout for **刷新状态**, **连接**, **断开**, and **离开房间**;
3. the log box in the only row that consumes remaining height;
4. a flow layout for **清空日志**, **复制日志**, and **导出日志**.

The flow layouts wrap buttons when horizontal space is constrained. Buttons use explicit safe minimum widths or their preferred content width so Chinese labels are not clipped. The log box docks to `Fill` within its row and retains both scroll bars and disabled word wrapping.

## Splitter and Sizing Policy

Use one layout-policy helper with no network or process side effects. Given the split container's available height and the current splitter position, it returns valid minimum panel sizes and a clamped splitter distance.

At normal sizes:

- the upper operation panel has a nominal minimum height of 250 pixels;
- the lower diagnostics panel has a nominal minimum height of 200 pixels;
- the initial splitter distance is the available split height multiplied by 45 percent and rounded to the nearest integer, then clamped to those minima;
- the upper panel is the fixed panel for ordinary window enlargement, so new vertical space primarily enlarges the log area.

When the available height is too small for both nominal minima plus the splitter width, the helper reduces the effective minima proportionally before assigning them. It must always ensure:

`upper minimum + lower minimum + splitter width <= available height`

The upper panel's scrolling remains the content fallback. The helper preserves a user-selected splitter distance while it remains valid and clamps it only when a resize makes it invalid. Switching between create and join pages does not reset it.

Run the clamp after initial layout, after relevant resizes, and after splitter movement. Guard against recursive layout events. Use `SuspendLayout` and `ResumeLayout` around multi-control changes to reduce flicker. Layout events must never invoke room, node-service, control-plane, or status-refresh operations and must not append UI log lines.

## Page and Refresh Lifecycle

The existing page lifecycle remains semantically unchanged:

- Welcome: show the welcome view, hide the operation shell, stop automatic status refresh.
- Host: show the operation shell and host content, ensure diagnostics is present in the lower split panel, perform the existing immediate automatic status refresh, and start the timer.
- Member: show the operation shell and member content, ensure diagnostics is present in the lower split panel, perform the existing immediate automatic status refresh, and start the timer.
- Shutdown: stop and dispose the timer before existing resource cleanup.

The resize implementation must not recreate controls or timers. It only asks WinForms to lay out the existing control tree.

## Error and Edge Handling

- A layout callback after disposal or during shutdown returns without accessing disposed controls.
- Invalid, negative, or temporarily zero layout dimensions return a safe no-op or bounded result rather than throwing.
- Setting `Panel1MinSize`, `Panel2MinSize`, or `SplitterDistance` occurs in an order that cannot violate WinForms split-container constraints.
- Larger fonts and higher DPI may increase the operation page's preferred size. The upper panel scrolls instead of allowing labels, inputs, or buttons to overlap.
- Long status text is clipped or ellipsized within its assigned header/status cell and must not cover the title.
- Button flow layouts may wrap vertically. Their rows auto-size so wrapped buttons cannot cover the log box.
- No resize or splitter exception may terminate the UI. Unexpected layout errors use existing safe error handling without exposing secrets.

## Component Boundaries

Implementation is limited to:

- `packaging/windows/ui.ps1` for the managed control hierarchy, responsive sizing policy, split-container behavior, and layout audit support;
- `cmd/ipv6mesh-installer/main_windows_test.go` for layout-policy and Windows UI regression coverage;
- `README.md` and `packaging/windows/README.md` only where their Windows UI behavior descriptions need updating.

Control-plane APIs, room enrollment, IPC, VPN service behavior, WireGuard data-plane behavior, live-status timing, log fingerprinting, and secret handling do not change.

## TDD Strategy

Add focused failing tests before changing the UI script.

### Structural regression tests

Tests must establish that:

1. the root form uses a fill-docked managed layout rather than direct placement of major views;
2. the operation shell uses a horizontal `SplitContainer`;
3. diagnostics is parented only in the lower split panel and is not positioned as a form-level sibling;
4. the upper split panel supports scrolling;
5. host, member, diagnostics, status/action, and log/action regions use managed layout containers;
6. the log box fills the remaining diagnostics row;
7. the problematic fixed diagnostics location and form-level fronting workaround are absent;
8. existing persistent-diagnostics and live-refresh behavior remains present.

### Pure sizing-policy tests

Extract the splitter calculation into a small pure function and invoke it from PowerShell-focused Go tests. Cover:

- normal initial sizing;
- preserving a valid user-selected distance;
- clamping a distance below the upper minimum;
- clamping a distance above the lower minimum;
- shrinking below the sum of nominal minima;
- growing after a constrained size;
- zero, negative, and very small available heights;
- the invariant that both panels plus splitter fit the available height.

### Headless WinForms layout audit

Provide a noninteractive audit path that builds the real control tree, performs layout without starting network resources, and returns only layout-safe measurements. Exercise welcome, host, and member views at:

- the preferred `1120 x 720` client size;
- the nominal safe minimum;
- the larger `1440 x 900` client size;
- a constrained work-area size;
- an enlarged UI font;
- splitter distances at both permitted extremes.

For each applicable state, assert:

- visible sibling controls do not have intersecting bounds unless they are intentional nested layout containers;
- the upper and lower split panels do not intersect;
- the primary create/join action is fully contained in the operation content or its scrollable virtual extent;
- the diagnostics group is fully contained in the lower panel;
- the log box has positive usable width and height and grows at the larger size;
- input fields grow horizontally at the larger size;
- wrapped action rows do not intersect the log row;
- diagnostics is absent on Welcome and visible on Host and Member.

The audit must not start the control server, node service, `vpnctl`, or automatic refresh timer and must not read or emit credentials.

## Verification

Implementation is complete only after fresh evidence for all of the following:

- focused RED evidence from the new structural, sizing-policy, and layout-audit tests;
- every new deterministic focused GREEN test repeated with `-count=20`;
- the existing diagnostics visibility, status-log decision, and quiet polling tests;
- `go test -count=1 ./...`;
- `go vet ./...`;
- PowerShell parser validation for every `packaging/windows/*.ps1` file;
- `GOOS=windows GOARCH=amd64 go test -run '^$' ./...`;
- `gofmt -l` reports no Go formatting drift;
- `git diff --check` reports no whitespace errors;
- the Windows installer rebuilds using the verified WireGuard DLL and license inputs;
- generated installer payload files are cleaned and are not committed.

Independent manual UI acceptance must launch the built WinForms UI and verify:

- Welcome, Host, and Member at the initial size;
- shrinking to the permitted minimum;
- enlarging the window;
- dragging the splitter to both limits;
- switching pages after choosing a splitter position;
- larger effective font or DPI behavior where the environment supports it;
- no button, label, input, diagnostics control, or log control overlaps or disappears;
- the timer still refreshes at roughly two-second intervals on operation pages and pauses on Welcome.

Real two-machine public-IPv6 and WireGuard connectivity acceptance remains a separate environment-dependent check and must not be claimed unless actually performed.
