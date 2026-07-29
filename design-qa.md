# Fleetty monitor pages — design QA

final result: passed

## Evidence

- Selected design source: `/Users/lianhaocheng/.codex/generated_images/019f9762-176a-77d2-81f1-3bbe404ecbde/call_idAzA3SE9wVpMKfCH77NjGPX.png`
- Implementation capture: `/tmp/fleetty-design-qa-overview.png`
- Combined comparison: `/tmp/fleetty-design-qa-comparison.png`
- Reference dimensions: 1586 × 992 px
- Implementation viewport: 160 × 40 terminal cells, rasterized at 1600 × 800 px
- State: dark theme, Linux GPU compute node, two GPUs, Slurm enabled, live network and process data

The implementation was captured from `monitorView` with deterministic realistic metrics. The ANSI output contains exactly 40 rendered terminal rows. The reference and implementation were placed in one side-by-side comparison image before judging visual differences.

## Full-view checks

- The global shell, four stable page tabs, capability summary, refresh/theme status and advanced customization entry are present.
- Overview follows the selected two-column hierarchy: Host Health beside Compute Activity, Network Activity beside Node Queue, then Top Processes.
- Battery is represented in the global status bar when available instead of consuming a separate card.
- GPU data appears only in Compute Activity; no duplicate GPU summary is shown.
- CPU history is integrated into Host Health; no separate System Timeline repeats the same metric.
- Network shows current receive/transmit rate and cumulative receive/transmit totals. Linux falls back to interface details when process attribution is unavailable.
- The process region absorbs remaining terminal height, so the footer finishes on the last terminal row instead of leaving a large empty lower area.
- Selected rows use a distinct dark selection background while process-state colors remain legible.

## Responsive and interaction checks

- Overview, Compute, Network and Processes fit at 160×40, 100×30 and 60×24 cells without horizontal overflow.
- Missing GPU and Slurm capabilities remove their regions and allow Host Health, Network Activity and Processes to reflow into the available width.
- Keyboard navigation works with `1`–`4`, `Tab`, `Shift+Tab`, arrows, Enter and `/`.
- Mouse page tabs and process rows use view-derived hitboxes.
- Advanced customization remains available through `l`; returning to a numbered page restores the designed shell.

## Findings and iteration history

- Initial implementation — P2: the Overview ended around row 29 in a 40-row terminal, leaving an unstructured lower area. Fixed by making the process preview consume the exact remaining viewport height and correcting section-gap row accounting.
- Initial implementation — P2: a one-GPU Compute panel had too much unused room. Fixed by showing renderer/tiler/core details for Apple GPUs and clock/power/temperature for NVIDIA GPUs.
- Initial implementation — P2: normal memory usage was labeled too aggressively. Fixed by reserving WATCH/HIGH/CRITICAL for 70/80/90 percent thresholds.
- Final comparison — P0: none.
- Final comparison — P1: none.
- Final comparison — P2: none.
- P3 intentional deviation: host OS and uptime are omitted until they are available in the shared snapshot model; placeholder values are not shown.
