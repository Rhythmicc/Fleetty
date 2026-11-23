#ifndef FLEETTY_PROGRESS_H
#define FLEETTY_PROGRESS_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define FLEETTY_PROGRESS_DEFAULT_INTERVAL_MS 1000ULL
#define FLEETTY_PROGRESS_PATH_MAX 108

typedef struct fleetty_progress {
    char socket_path[FLEETTY_PROGRESS_PATH_MAX];
    uint64_t min_interval_ns;
    uint64_t last_sent_ns;
    int last_fd;
    int auto_discover;
} fleetty_progress_t;

/*
 * Initialize a progress reporter.
 *
 * Pass NULL or "" for socket_path to auto-discover Fleetty's socket on first
 * report (FLEETTY_PROGRESS_SOCKET env var, then per-user socket, then Linux
 * system socket).
 *
 * Pass FLEETTY_PROGRESS_DEFAULT_INTERVAL_MS (1000 ms) for tight compute loops,
 * or 0 to send on every fleetty_progress_report() call.
 */
void fleetty_progress_init(
    fleetty_progress_t *p,
    const char *socket_path,
    uint64_t min_interval_ms
);

/*
 * Report progress non-blocking (fire-and-forget).
 *
 * Respects min_interval_ms configured in fleetty_progress_init().
 * Returns:
 *    1  update sent over the Unix socket
 *    0  skipped because min_interval_ms has not elapsed since last attempt
 *   -1  invalid arguments or Fleetty socket unavailable (safe to ignore)
 */
int fleetty_progress_report(
    fleetty_progress_t *p,
    uint64_t done,
    uint64_t total,
    const char *label
);

/*
 * Send an update immediately regardless of min_interval_ms (e.g. phase switch
 * or 100% completion). Updates the throttle timestamp.
 *
 * Returns 1 on send success, or -1 on invalid arguments / unavailable socket.
 */
int fleetty_progress_report_force(
    fleetty_progress_t *p,
    uint64_t done,
    uint64_t total,
    const char *label
);

/*
 * Clear the active progress bar for this process immediately and reset the
 * throttle timer. Returns 0 on success, -1 if unavailable.
 */
int fleetty_progress_clear(fleetty_progress_t *p);

/*
 * Release internal descriptor state (deferred close). Safe to call repeatedly.
 */
void fleetty_progress_close(fleetty_progress_t *p);

/*
 * Write the default per-user progress socket path for the current effective UID
 * into buf. Returns 0 on success, -1 if buf is NULL or too small.
 */
int fleetty_progress_default_path(char *buf, size_t buf_size);

#ifdef __cplusplus
}
#endif

#endif /* FLEETTY_PROGRESS_H */
