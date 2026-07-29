# Fleetty monitor pages — design QA

final result: passed

## Evidence

- Selected design source: `/Users/lianhaocheng/.codex/generated_images/019f9762-176a-77d2-81f1-3bbe404ecbde/call_idAzA3SE9wVpMKfCH77NjGPX.png`
- Implementation capture: `/tmp/fleetty-rich-overview.png`
- Combined comparison: `/tmp/fleetty-rich-comparison.png`
- Alignment report source: `/var/folders/zm/1n48rm8n34b767q9b7k_46sm0000gn/T/codex-clipboard-51c194ad-91a4-405b-8993-d1ebd1c245c6.png`
- Alignment fix capture: `/tmp/fleetty-alignment-fixed.png`
- Alignment comparison: `/tmp/fleetty-alignment-comparison.png`
- Reference dimensions: 1586 × 992 px
- Implementation viewport: 160 × 40 terminal cells, rasterized at 1807 × 956 px with one non-rendered guard column
- State: dark theme, Linux GPU compute node, two GPUs, Slurm enabled, live network and process data

The implementation was captured from `monitorView` with deterministic realistic metrics. The ANSI output contains exactly 40 rendered terminal rows. The reference and implementation were placed in one side-by-side comparison image before judging visual differences.

## Full-view checks

- The global shell shows host identity, OS, uptime, Slurm state, capabilities, refresh/theme status and four stable page tabs.
- Overview follows the selected two-column hierarchy: Host Health beside Compute Activity, Network Activity beside Node Queue, then Top Processes.
- Battery is represented in the global status bar when available instead of consuming a separate card.
- GPU data appears only in Compute Activity; no duplicate GPU summary is shown.
- NVIDIA Compute Activity includes live load, memory, clock, power, temperature, shortened UUID, active workload owner and driver version.
- CPU history is integrated into Host Health; no separate System Timeline repeats the same metric.
- Network shows current receive/transmit rate, cumulative totals, distinct 60-second RX/TX histories, peaks and attributed applications or interfaces.
- Node Queue summarizes running, next and queued jobs, then shows a bounded job preview and collection age.
- The process region is capped at ten rows and uses only the remaining terminal height; the dedicated Processes page remains the full-list surface.
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
- Post-release report — P2: Apple GPU metric columns were offset because ANSI-styled labels were padded by byte length instead of visible terminal width. Fixed with a shared visible-width metric row formatter; bars now begin at column 9 and values at column 28.
- Richness audit — P1: the first implementation reproduced panel positions but collapsed the reference's information hierarchy. The header omitted OS, uptime and capability context; Host Health omitted CPU identity and labeled load windows; Network lacked distinct history and attribution structure; Compute omitted NVIDIA workload identity; and the process list consumed most of the page. Fixed by enriching each overview module, adding capability-backed metadata, and bounding the process preview.
- Richness audit — P2: a deterministic Slurm capture displayed an incorrect multi-hour refresh age because the view compared fixture time with wall-clock time. Fixed by measuring queue age against the snapshot collection time.
- Final comparison — P0: none.
- Final comparison — P1: none.
- Final comparison — P2: none.
- P3 intentional deviation: CUDA, NCCL, NVLink and MIG versions are not inferred from the driver version; they are omitted until a platform collector reports them reliably.
