English | [简体中文](../zh-CN/usage.md)

[Back to the README](../../README.md)

# Using Fleetty

## Pages

The default interface has four pages: Overview, Compute, Network, and Storage. Press `1` through `4`, or move with `Tab` and `Shift+Tab`. Left and Right do the same, except on the storage map, where they navigate directories.

Overview puts the host and its uptime, CPU, memory, and disk health, GPU load, a 60-second network history, application traffic, the Slurm node queue, and a short process summary on one screen. Compute gives every remaining row to processes, after the hardware and the queue. Network shows live rates, cumulative totals, a 60-second peak, application traffic, and per-interface live and cumulative counters.

Fleetty draws only what this machine can supply. A host with no GPU, or no Slurm, drops that region, and the panels around it reflow into the space. Arrow keys, Enter, and the mouse select a process and open its details. `/` filters processes. `t` changes the theme. `q` quits.

`/` from Network or Storage moves to Compute before the filter opens. On a custom page it also expands the process panel if that panel was hidden.

## Keys

| Key | Action |
| --- | --- |
| `1`–`3` | Overview, Compute, Network |
| `4` | Local Storage Map |
| `Tab` / `Shift+Tab` | Next / previous page |
| `↑` / `↓`, click | Select a process |
| `Enter` | Read-only process details |
| `/` / `c` | Filter / clear the filter |
| `o` / `O`, click a column header | Next sort field / reverse the order |
| `PageUp` / `PageDown`, scroll wheel | Scroll the focused panel |
| `Home` / `End` | Top / bottom of the focused panel |
| `l` | Layout editor |
| `m` | Management mode |
| `t` | Choose the theme for this session |
| `r` / `q` | Refresh / quit |

Theme, page, filter, and sort belong to the session. A remote read-only detail redacts common passwords, tokens, and similar arguments. The full detail, and any write, requires [management mode](security.md).

On the duplicate view of the storage map, `c` chooses which copy to keep. It does not clear the process filter there.

## CPU and history

The CPU widget has three sizes. SMALL is host utilization, load, and a history. MEDIUM adds per-core load in the same panel. LARGE adds the busiest processes as well. Compute (`2`) opens on the per-core form (`CPU · PER CORE`) and does not add a separate CPU CORES card. A custom layout picks the size by the widget's setting.

Linux and macOS both number logical CPUs the way the kernel does, and show each core's live utilization from 0 to 100 percent, with a bar. Hyper-threads are separate cores, not a physical topology. The full list scrolls with `PgUp` and `PgDn` and is not truncated. The first sample, a core that just came online, or a counter reset shows `--` until the next good sample. If per-core data is missing, the panel says so. The overview keeps the host-wide average. Utilization is the difference between two CPU-time samples. Idle and iowait are not busy time. The 1, 5, and 15 minute load averages stay a separate host metric.

A JSON snapshot's `cpu_core_usage` carries the CPU id, `percent`, and `available`. Prometheus exports `fleetty_cpu_core_percent{node="…",cpu="cpu0"}`. The Hub and the node both need a build that understands these fields.

Above 32 logical CPUs, Compute uses a compact vertical chart. At 32 or fewer it keeps the horizontal bar per core. Each core is one column, ordered by CPU id, never resorted by load. Width follows the terminal. Height and color both mean 0–100 percent. A baseline `▁` is zero. `?` means not sampled yet. The history moves into a narrow column on the right, so a mid-size machine is not squeezed to the left. The chart is at most two rows. A narrow window, or a very large core count, pages with the selected core. Samples are not dropped. Click a column, or press `f` and move with the arrows, `Home`, and `End`. The line under the chart shows the id, the load, and the GHz. `Esc` or `f` returns to the process keys. `v` toggles the column chart and the full per-core list. That choice is only for this session's Compute page. Collection, JSON, Prometheus, and a custom layout are unchanged.

Every history is a multi-line bar chart filled from the bottom. The top reads `HIST ·` plus the metric and its unit. The bottom reads `old → now`. CPU is fixed at 0–100 percent. Network RX and TX share the peak of the window, and that peak is labeled. Downsampling covers the whole retained window. A missing sample is a gap, not a zero and not a floating dot. If the window is empty, the chart waits. Overview keeps one recent CPU history, not a second 1-hour chart beside it. The older samples are still stored. Network, overview, custom cards, and the NAS view use the same widget. A wide window places series side by side. A narrow one stacks them, or drops a secondary history. It does not fall back to a one-line sparkline, and it does not change the live load bar. The overview network summary uses the space beside the chart for up to five lines, or three when narrow, without growing the panel. Memory and the root disk use a short bar. Extra rows go to the process table.

The CPU panel also updates clocks on every sample. `AVG` is the arithmetic mean of online logical CPUs that have a frequency sample. The per-core view prints GHz next to the load, plus the range and, when the sample is partial, how many cores contributed. The per-core header folds the summary, load, and metadata into two lines, or three when the window is narrow. The history sits beside the cores. The source of the reading is kept in the export, not in that header.

On Linux the value is `cpu MHz` from `/proc/cpuinfo`, the kernel's current frequency. On an Apple Silicon Mac, with no sudo, Fleetty reads IOReport directly and weights each core's active residency across the DVFS states reported by IORegistry. The source is `IOReport · active residency`. It is not a register read. The first sample establishes a baseline. A core that was fully idle, or whose counters reset, shows `--`. Fleetty does not fill the gap with the minimum, nominal, maximum, or requested frequency. If the interface is missing, the states cannot be matched, or the machine is an Intel Mac or a VM that does not publish the data, the panel shows `CLK --` rather than 0 GHz. IOReport is a private interface, and a system update can change it. On Linux the precision depends on the kernel, the hardware, and the hypervisor.

JSON `cpu_frequency` has the mean, minimum and maximum MHz, the sample count, and the source. `cpu_core_usage[].frequency_mhz` is the per-core MHz. Prometheus exports `fleetty_cpu_frequency_mhz` and `fleetty_cpu_core_frequency_mhz`, along with the min, max, and sampled-core gauges. A Hub detail view uses the same data, so the node has to be running a build that samples it.

An Apple Silicon Mac also shows, through IOKit and without root, total GPU load, renderer and tiler utilization, the core count, and unified memory in use and allocated. A MacBook's charge, charge or discharge state, time remaining, and power source sit in the top bar. A desktop Mac with no battery omits them. This mode starts no background service, opens no port, and does not need root.

## Processes

Click a column header to sort by CPU, memory percent, RSS, elapsed time, PID, user, or command. Click the same header again to flip the direction. `o` moves to the next field and `O` reverses the current one. Neither needs management mode. The row range and the total are shown on the rows and in the corner of the panel. A filter adds the match count. Each connection keeps its own sort, and a refresh keeps the selected PID.

CPU, memory, and RSS move from gray through cyan, green, yellow, and red as the value rises. The process name does not change color with the process state. `100%` CPU means one logical core. Memory percent and RSS are colored against the memory of the whole machine. The mark before the PID means running (`▶`), sleeping (`●`), waiting (`!`), stopped (`■`), zombie or exited (`×`), or idle (`○`). The table includes the legend.

## Network

On Linux, the Network page and a large network widget list TCP and UDP sockets for IPv4 and IPv6: PID, name, user, and live DOWN/UP. When the width allows, they add protocol, state, and the local and remote addresses. Each TCP connection's rate is the difference of the cumulative byte counters from Linux `INET_DIAG` / `TCP_INFO`, refreshed every second. Connections with traffic sort first. Several connections of one process are counted separately. The socket cookie stops a reused port from inventing a rate. The first sample says `sample…`. A counter reset starts over. The rate is TCP payload, without headers, and it is not the wire rate of the NIC. UP uses bytes sent, which can include retransmits. UDP, listening sockets, `TIME_WAIT`, and any socket whose counters the kernel will not provide show `--` instead of a fake zero. Exact UDP byte counts would need eBPF or a capture, and that collector is not enabled.

The numbers come from the current network namespace. A normal user may not see which process owns another user's socket. The PID is then `--`, with a note about permissions. The user name prefers the socket UID from the kernel, and shows the numeric UID when the name does not resolve. Otherwise it uses the user of a process Fleetty already knows. macOS still uses `nettop` for per-process rates and totals.

`fleetty snapshot --processes` exports the same connection list, user, cumulative bytes, rates, and `traffic_available` / `rate_available`, so an unsampled rate is distinct from a real zero.

The Linux connection list is ordered by live `DOWN + UP`, descending, not by one direction and not by the cumulative total. Equal rates keep a stable order. Each direction is colored by its own absolute rate: unavailable, or below 1 KiB/s, is gray. From 1 KiB/s the direction color is used, cyan for down and purple for up. From 100 KiB/s the cell is green, from 1 MiB/s yellow, and from 10 MiB/s orange. The table's legend states the sort and the colors. The colors are magnitude. They are not saturation or a fault, because a connection does not know the capacity of its link.

## Storage map

The Storage page draws the directory as a treemap. Area is allocated disk space. So that a crowd of tiny files does not become an unreadable speckle, entries are paged by size. A page holds at most 16 real entries you can click, and a small terminal holds fewer, down to 4. `PgUp`, `PgDn`, or the pager above the map continues into the smaller files.

A scan of the current directory's immediate children uses one worker when there is only one child, and otherwise between 2 and 8 workers, following `GOMAXPROCS`. Size, file count, and tiles update while the scan is still running. You can click a directory that has already appeared. Fleetty cancels the current work and enters that directory instead of finishing an unrelated scan. The arrows and Enter do the same. `Backspace`, Left, or `h` goes to the parent. Right opens the selection. `Home` returns to the scan root.

Directories you have visited stay in a bounded cache for this session. Going back restores a complete, fresh map immediately. A stale or unfinished result is shown first and rescanned in the background. `r` forces a rescan of the current directory. Inside the duplicate view, `r` rescans duplicates instead.

With an entry selected, `d` deletes it permanently and `z` writes a `.7z` archive and then deletes the original. Each opens its own confirmation and requires the phrase `DELETE` or `ARCHIVE`. Archiving needs `7z`, `7zz`, or `7za` on `PATH`. Fleetty creates the archive in a temporary file on the same filesystem, checks it with 7z, and publishes it without overwriting an existing file. The original is removed only after all of that succeeds. A failed archive leaves the original untouched. If the archive was published and the delete then fails, the screen reports both the archive path and the original that is still there. Once a delete has started, it cannot be interrupted. An archive can be cancelled with `Esc` until it reaches a safe stop. The scan root, a symlink, a mount point, and a directory that contains another mount point cannot be deleted.

`u` scans the current directory for duplicate files. Fleetty groups candidates by size, then hashes them with SHA-256 using up to 8 workers. By default only regular files of at least 1 MiB are considered. Existing hard links are not counted twice. Symlinks, network mounts, and virtual filesystems are skipped. Groups are ordered by reclaimable space, with the path to keep and the paths to replace. `c` cycles the file this group will keep. `g` writes a dedupe script for the current group. `G` writes one for every verified group. Press `u` again to leave the duplicate view.

Scripts are saved under the user configuration directory in `fleetty/dedupe-plans`, mode `0700`. On Linux that directory is `~/.config` (or `$XDG_CONFIG_HOME`). On macOS it is `~/Library/Application Support`. The script is not executed. Read the paths it will keep before you run it. When it runs, every file is checked again: SHA-256, identity, owner, mode, and extended attributes or ACL. Fleetty then replaces the duplicate atomically inside its own directory, deletes only the verified copy, and leaves a relative symlink to the file that was kept. If a check, a link, or the publish step fails, it tries to restore the original copy. Afterward, those paths depend on the kept file. Moving or deleting it breaks the symlinks, so read a batch script before you run it.

As a normal user, the scan and the file actions stay inside that user's home, and inside the permissions the user already has. `sudo fleetty top`, or running as root, scans `/` and still refuses to delete `/` itself. The scan does not follow symlinks, and it skips network mounts such as NFS, SMB, and SSHFS, and virtual filesystems such as `/proc` and `/sys`. The page and the file actions exist only in local `fleetty top`. A remote session stays read-only and never receives the service account's ability to change files.

## Custom layout

`l` opens the widget editor. CPU, memory, disk, network, battery, GPU, the node queue, and processes can be reordered, hidden, and set to `SMALL`, `MEDIUM`, or `LARGE`. A large widget adds metrics and table rows. It does not only stretch the chart. On macOS, a large network widget uses `nettop` to show live and cumulative traffic by application and process. Editing the layout does not require management mode.

Uppercase `K` and `J` move the selected widget. Lowercase `k` and `j`, or the arrow keys, only move the cursor. `-` and `+` change the size. Space hides or shows the widget. The mouse works on the same controls. Leaving the editor opens the custom page. `1` through `4` return to a built-in page whenever you want. `s` saves the layout for the local user. A remote session has nowhere to save, and the editor says so. The default file is `layout.json` inside `fleetty/` under the user configuration directory. `--layout PATH` or `FLEETTY_LAYOUT_FILE` selects another file.

A NAS profile cannot open this editor yet. `l` reports that custom NAS layouts are not available.
