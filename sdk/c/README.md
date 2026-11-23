# Fleetty Progress C SDK (`sdk/c`)

Zero-dependency C99 / C++ source package for fast, non-blocking, fire-and-forget process progress reporting to `fleetty` or `fleetty top`.

## Features

- **Drop-in two files**: Copy `fleetty_progress.h` and `fleetty_progress.c` directly into your project (or build `libfleetty_progress.a` with `make`).
- **Built-in hot-loop throttling**: Safe to call on every loop iteration; calls within `min_interval_ms` (recommended `FLEETTY_PROGRESS_DEFAULT_INTERVAL_MS` = 1000 ms) skip formatting and syscalls via `CLOCK_MONOTONIC` / `CLOCK_MONOTONIC_COARSE`.
- **Non-blocking fire-and-forget**: Sends one newline-terminated JSON update over `AF_UNIX` without waiting for a reply, using deferred close so peer-credential verification remains reliable on both Linux and macOS.
- **Zero heap allocations & `SIGPIPE`-safe**: Formats JSON on the stack with full UTF-8 boundary clamping (256-byte limit) and escaping.
- **Auto-discovery**: Passing `NULL` for `socket_path` checks `FLEETTY_PROGRESS_SOCKET`, then the per-user socket (`/dev/shm/fleetty-progress-v1-<uid>/progress.sock` on Linux or `/tmp/fleetty-progress-v1-<uid>/progress.sock` on macOS), and finally `/run/fleetty-progress-v1.sock` for non-root Linux processes.

## Quick Start

Copy `fleetty_progress.h` and `fleetty_progress.c` into your build, then:

```c
#include "fleetty_progress.h"

int main(void) {
    fleetty_progress_t progress;
    fleetty_progress_init(&progress, NULL, FLEETTY_PROGRESS_DEFAULT_INTERVAL_MS);

    for (uint64_t step = 0; step < total; step++) {
        do_work(step);
        fleetty_progress_report(&progress, step, total, "factor");
    }

    fleetty_progress_report_force(&progress, total, total, "done");
    fleetty_progress_close(&progress);
    return 0;
}
```

Compile alongside your C or C++ code:

```sh
cc -O2 -std=c99 your_app.c sdk/c/fleetty_progress.c -o your_app
```

Or build the static library and run tests:

```sh
make -C sdk/c all test
```

## API Summary

- `fleetty_progress_init(&p, socket_path, min_interval_ms)` — configure target socket (`NULL` for auto-discovery) and throttle window (`FLEETTY_PROGRESS_DEFAULT_INTERVAL_MS` = `1000`).
- `fleetty_progress_report(&p, done, total, label)` — throttled non-blocking report (`1` sent, `0` throttled, `-1` invalid/unavailable).
- `fleetty_progress_report_force(&p, done, total, label)` — bypass throttle (useful on phase changes or completion) and update the throttle timestamp.
- `fleetty_progress_clear(&p)` — immediately remove the progress bar for a long-lived process that continues running.
- `fleetty_progress_close(&p)` — close any held socket descriptor.
- `fleetty_progress_default_path(buf, buf_size)` — query the OS-specific default per-user socket path.

## Performance Tips

- Pass `FLEETTY_PROGRESS_DEFAULT_INTERVAL_MS` (`1000`) or higher (`1000`–`5000` ms) when calling inside tight loops.
- If the socket does not exist (for example when `fleetty` is not running), `fleetty_progress_report()` updates its throttle timestamp even on failure, preventing per-iteration `connect()` retries inside compute loops.
