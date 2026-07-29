# Fleetty three-page monitor — design QA

final result: passed

## Evidence

- Source visual truth: `/var/folders/zm/1n48rm8n34b767q9b7k_46sm0000gn/T/codex-clipboard-66ec1a07-dd71-44d1-91d3-40db1d87677c.png`
- Rendered implementation: `/tmp/fleetty-network-page.png`
- Full-view comparison: `/tmp/fleetty-network-comparison.png`
- Focused Network comparison: `/tmp/fleetty-network-focused-comparison.png`
- Source pixels: 3248 × 2220
- Implementation pixels: 1760 × 920
- Implementation terminal viewport: 160 × 40 cells, rasterized at 11 × 23 pixels per cell
- Normalization: the source terminal content was cropped from its desktop canvas and resized to the implementation width; the focused comparison places the old and new Network content side by side at 1760 pixels per region.
- State: dark theme, Network page selected, 12 attributed processes, 31 interfaces, live and cumulative traffic available.

## Full-view checks

- The numbered navigation contains only Overview, Compute and Network. The removed Processes page no longer consumes a tab, key binding or mouse target.
- The Network page fills all 40 terminal rows, including the footer, without the large unstructured lower region visible in the source report.
- Network information is organized into three adjacent vertical panels: live activity, attributed applications and interface counters. Panel borders touch without blank separator rows.
- Current receive/transmit rates, cumulative received/sent totals, 60-second histories, peaks, traffic mix, primary interface, attribution count and aggregate error/drop count are visible in the summary.
- The application table provides PID, name, current down/up rates and cumulative total.
- The interface table provides current down/up rates, cumulative RX/TX totals and error/drop counts.
- Compute uses its remaining panel height for process rows instead of stopping at a fixed twelve-row limit. A 160 × 55 regression fixture renders more than twelve interactive rows and ends exactly on the terminal footer.

## Required fidelity surfaces

- Fonts and typography: the implementation retains Fleetty's existing monospace hierarchy, bold panel titles, dim metadata and cyan/purple traffic semantics. Headers and numeric columns remain visually distinct and unwrapped.
- Spacing and layout rhythm: all three Network panels and all Compute sections are directly adjacent; both pages calculate their content from the current terminal height and fill the viewport.
- Colors and visual tokens: RX remains cyan, TX purple, live state mint and metadata muted gray. Contrast is consistent with the existing dark theme.
- Image quality and asset fidelity: Fleetty is a native terminal surface with no raster product assets. The capture is rasterized directly from ANSI output at a fixed cell grid without scaling the implementation.
- Copy and content: labels distinguish current rates from cumulative totals and use explicit `RX TOTAL`, `TX TOTAL`, `ERRORS/DROPS`, `ATTRIBUTED` and `60 SECOND WINDOW` terminology.

## Responsive and interaction checks

- Overview, Compute and Network render at exactly 160 × 40, 100 × 30 and 60 × 24 cells without horizontal overflow.
- `1`–`3`, Tab, Shift+Tab and mouse page tabs navigate only the three designed pages; key `4` no longer changes pages.
- Process selection, details and filtering remain available on Overview and Compute. Invoking `/` from Network moves to Compute before opening the filter.
- Compute process hitboxes use the dynamically allocated row count.

## Findings and comparison history

- Source report — P1: the dedicated Processes page duplicated a workload already present elsewhere and made the page model unnecessarily broad. Fixed by reducing navigation to three designed pages and making Compute the full process surface.
- Source report — P1: Network ended near the middle of the terminal and left a large empty lower viewport. Fixed by budgeting all remaining rows across summary, application and interface panels.
- Source report — P2: application rows omitted down/up/total values in the wide layout, while interface rows omitted cumulative traffic. Fixed with full-width, aligned tables for both data sets.
- Source report — P2: Compute capped workloads at twelve rows regardless of terminal height. Fixed by deriving process rows from the exact remaining viewport and binding keyboard/mouse bounds to that count.
- Post-fix evidence: the 160 × 40 Network capture reaches the footer exactly, shows ten attributed applications and eleven interface rows, and has no unused page region. The focused comparison makes the added traffic columns and removal of the old sparse split layout visible.
- Final comparison — P0: none.
- Final comparison — P1: none.
- Final comparison — P2: none.
- P3 intentional behavior: very tall terminals devote additional rows to the interface table after all attributed applications are visible; this preserves real data density instead of padding another summary chart.
