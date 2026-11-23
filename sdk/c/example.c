#include "fleetty_progress.h"

#include <unistd.h>

int main(void) {
    fleetty_progress_t progress;
    const uint64_t total = 200;

    fleetty_progress_init(&progress, NULL, FLEETTY_PROGRESS_DEFAULT_INTERVAL_MS);

    for (uint64_t step = 0; step < total; step++) {
        /* Safe to call inside tight loops: throttled to min_interval_ms in C. */
        fleetty_progress_report(&progress, step, total, "factor");
        usleep(20000);
    }

    fleetty_progress_report_force(&progress, total, total, "done");
    fleetty_progress_clear(&progress);
    fleetty_progress_close(&progress);
    return 0;
}
