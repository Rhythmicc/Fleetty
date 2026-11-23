#if defined(__linux__) && !defined(_GNU_SOURCE)
#define _GNU_SOURCE
#endif

#include "fleetty_progress.h"

#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

static int listen_temp_sock(char *dir_buf, size_t dir_cap, char *sock_buf, size_t sock_cap) {
    int n = snprintf(dir_buf, dir_cap, "/tmp/flprog-test-XXXXXX");
    assert(n > 0 && (size_t)n < dir_cap);
    char *dir = mkdtemp(dir_buf);
    assert(dir != NULL);

    n = snprintf(sock_buf, sock_cap, "%s/p.sock", dir);
    assert(n > 0 && (size_t)n < sock_cap);

    int lfd = socket(AF_UNIX, SOCK_STREAM, 0);
    assert(lfd >= 0);
    int flags = fcntl(lfd, F_GETFL, 0);
    assert(flags >= 0 && fcntl(lfd, F_SETFL, flags | O_NONBLOCK) == 0);

    struct sockaddr_un addr;
    memset(&addr, 0, sizeof(addr));
    addr.sun_family = AF_UNIX;
    assert(strlen(sock_buf) < sizeof(addr.sun_path));
    memcpy(addr.sun_path, sock_buf, strlen(sock_buf) + 1);
    assert(bind(lfd, (const struct sockaddr *)&addr, sizeof(addr)) == 0);
    assert(listen(lfd, 16) == 0);
    return lfd;
}

static size_t accept_and_read(int lfd, char *buf, size_t cap, int write_reply) {
    struct pollfd pfd = { .fd = lfd, .events = POLLIN, .revents = 0 };
    assert(poll(&pfd, 1, 1000) == 1);
    int cfd = accept(lfd, NULL, NULL);
    assert(cfd >= 0);

    size_t total = 0;
    struct pollfd cpfd = { .fd = cfd, .events = POLLIN, .revents = 0 };
    while (total + 1 < cap) {
        if (poll(&cpfd, 1, 500) <= 0) {
            break;
        }
        ssize_t r = read(cfd, buf + total, cap - 1 - total);
        if (r > 0) {
            total += (size_t)r;
            if (memchr(buf, '\n', total) != NULL) {
                break;
            }
            continue;
        }
        if (r < 0 && errno == EINTR) {
            continue;
        }
        break;
    }
    buf[total] = '\0';
    if (write_reply) {
        const char ok[] = "{}\n";
#ifdef SO_NOSIGPIPE
        int one = 1;
        (void)setsockopt(cfd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof(one));
#endif
        int flags_send = 0;
#ifdef MSG_NOSIGNAL
        flags_send = MSG_NOSIGNAL;
#endif
        (void)send(cfd, ok, sizeof(ok) - 1, flags_send);
    }
    close(cfd);
    return total;
}

static void test_default_path(void) {
    char small[4];
    assert(fleetty_progress_default_path(NULL, 10) == -1);
    assert(fleetty_progress_default_path(small, sizeof(small)) == -1);

    char buf[FLEETTY_PROGRESS_PATH_MAX];
    assert(fleetty_progress_default_path(buf, sizeof(buf)) == 0);
    assert(strstr(buf, "fleetty-progress-v1") != NULL);
}

static void test_validation_and_missing_socket_throttle(void) {
    fleetty_progress_t p;
    assert(fleetty_progress_report(NULL, 1, 10, "x") == -1);
    fleetty_progress_init(&p, "/tmp/definitely-missing-fleetty-progress-test.sock", 5000);
    assert(fleetty_progress_report(&p, 0, 0, NULL) == -1);
    assert(fleetty_progress_report(&p, 11, 10, NULL) == -1);
    assert(fleetty_progress_report(&p, 1, 10, NULL) == -1);
    assert(p.last_sent_ns != 0);
    /* Subsequent call within min_interval_ms stays throttled even after connect failure. */
    assert(fleetty_progress_report(&p, 2, 10, NULL) == 0);
    fleetty_progress_close(&p);
    assert(p.last_fd == -1);
}

static void test_wire_protocol_and_utf8_clamping(void) {
    char dir[64];
    char sock[96];
    int lfd = listen_temp_sock(dir, sizeof(dir), sock, sizeof(sock));

    fleetty_progress_t p;
    fleetty_progress_init(&p, sock, 5000);

    /* 1. Plain report without label + throttle + deferred close */
    assert(fleetty_progress_report(&p, 10, 100, NULL) == 1);
    assert(p.last_fd >= 0);
    assert(fleetty_progress_report(&p, 11, 100, "ignored") == 0);

    char msg[2048];
    accept_and_read(lfd, msg, sizeof(msg), 1);
    assert(strcmp(msg, "{\"v\":1,\"done\":10,\"total\":100}\n") == 0);

    /* 2. Forced update with escaped quotes, backslashes, and control chars */
    assert(fleetty_progress_report_force(&p, 25, 100, "a\"b\\c\nd\te\x01") == 1);
    accept_and_read(lfd, msg, sizeof(msg), 1);
    assert(strcmp(msg, "{\"v\":1,\"done\":25,\"total\":100,\"label\":\"a\\\"b\\\\c\\nd\\te\\u0001\"}\n") == 0);

    /* 3. UTF-8 clamping near 256 bytes: 85 * 3-byte '中' = 255 bytes + 3-byte '文' -> must keep 255 bytes */
    char long_utf8[300];
    size_t off = 0;
    for (int i = 0; i < 85; i++) {
        long_utf8[off++] = (char)0xE4;
        long_utf8[off++] = (char)0xB8;
        long_utf8[off++] = (char)0xAD;
    }
    long_utf8[off++] = (char)0xE6;
    long_utf8[off++] = (char)0x96;
    long_utf8[off++] = (char)0x87;
    long_utf8[off] = '\0';

    assert(fleetty_progress_report_force(&p, 50, 100, long_utf8) == 1);
    size_t n = accept_and_read(lfd, msg, sizeof(msg), 1);
    assert(strstr(msg, "\"done\":50") != NULL);
    /* Verify label ends on exact 255-byte boundary (last '中', never partial '文'). */
    assert(strstr(msg, "\xE4\xB8\xAD\"}\n") != NULL);
    assert(n < 320);

    /* 4. Clear message drains reply cleanly and closes socket */
    assert(fleetty_progress_clear(&p) == 0);
    assert(p.last_fd == -1);
    assert(p.last_sent_ns == 0);
    accept_and_read(lfd, msg, sizeof(msg), 1);
    assert(strcmp(msg, "{\"v\":1,\"clear\":true}\n") == 0);

    /* 5. SIGPIPE resilience when peer closes immediately without reading */
    assert(fleetty_progress_report_force(&p, 60, 100, "early-close") == 1);
    struct pollfd pfd = { .fd = lfd, .events = POLLIN, .revents = 0 };
    assert(poll(&pfd, 1, 500) == 1);
    int cfd = accept(lfd, NULL, NULL);
    assert(cfd >= 0);
    close(cfd);
    /* Next report after peer closed must still work cleanly without SIGPIPE crash. */
    assert(fleetty_progress_report_force(&p, 70, 100, "recovered") == 1);
    accept_and_read(lfd, msg, sizeof(msg), 1);
    assert(strstr(msg, "\"done\":70") != NULL);

    fleetty_progress_close(&p);
    close(lfd);

    /* 6. Auto-discovery via FLEETTY_PROGRESS_SOCKET env var */
    unlink(sock);
    rmdir(dir);

    lfd = listen_temp_sock(dir, sizeof(dir), sock, sizeof(sock));
    assert(setenv("FLEETTY_PROGRESS_SOCKET", sock, 1) == 0);
    fleetty_progress_init(&p, NULL, 0);
    assert(fleetty_progress_report(&p, 99, 100, "env") == 1);
    accept_and_read(lfd, msg, sizeof(msg), 1);
    assert(strcmp(msg, "{\"v\":1,\"done\":99,\"total\":100,\"label\":\"env\"}\n") == 0);
    fleetty_progress_close(&p);
    unsetenv("FLEETTY_PROGRESS_SOCKET");

    close(lfd);
    unlink(sock);
    rmdir(dir);
}

int main(void) {
    test_default_path();
    test_validation_and_missing_socket_throttle();
    test_wire_protocol_and_utf8_clamping();
    printf("fleetty_progress C tests passed.\n");
    return 0;
}
