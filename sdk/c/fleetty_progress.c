#if defined(__linux__) && !defined(_GNU_SOURCE)
#define _GNU_SOURCE
#endif
#if defined(__APPLE__) && !defined(_DARWIN_C_SOURCE)
#define _DARWIN_C_SOURCE
#endif

#include "fleetty_progress.h"

#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#define FLEETTY_LABEL_RAW_MAX 256
#define FLEETTY_PAYLOAD_CAP 2048

static uint64_t fleetty_monotonic_ns(void) {
    struct timespec ts;
#ifdef CLOCK_MONOTONIC_COARSE
    if (clock_gettime(CLOCK_MONOTONIC_COARSE, &ts) == 0) {
        uint64_t ns = (uint64_t)ts.tv_sec * 1000000000ULL + (uint64_t)ts.tv_nsec;
        return ns ? ns : 1ULL;
    }
#endif
    if (clock_gettime(CLOCK_MONOTONIC, &ts) == 0) {
        uint64_t ns = (uint64_t)ts.tv_sec * 1000000000ULL + (uint64_t)ts.tv_nsec;
        return ns ? ns : 1ULL;
    }
    return 1ULL;
}

int fleetty_progress_default_path(char *buf, size_t buf_size) {
    if (buf == NULL || buf_size == 0) {
        return -1;
    }
    uid_t uid = geteuid();
    int n;
#if defined(__linux__)
    if (uid == 0) {
        n = snprintf(buf, buf_size, "/run/fleetty-progress-v1.sock");
    } else {
        n = snprintf(buf, buf_size, "/dev/shm/fleetty-progress-v1-%lu/progress.sock",
                     (unsigned long)uid);
    }
#else
    n = snprintf(buf, buf_size, "/tmp/fleetty-progress-v1-%lu/progress.sock",
                 (unsigned long)uid);
#endif
    if (n < 0 || (size_t)n >= buf_size) {
        buf[0] = '\0';
        return -1;
    }
    return 0;
}

void fleetty_progress_init(
    fleetty_progress_t *p,
    const char *socket_path,
    uint64_t min_interval_ms
) {
    if (p == NULL) {
        return;
    }
    memset(p, 0, sizeof(*p));
    p->last_fd = -1;
    if (min_interval_ms > UINT64_MAX / 1000000ULL) {
        p->min_interval_ns = UINT64_MAX;
    } else {
        p->min_interval_ns = min_interval_ms * 1000000ULL;
    }
    if (socket_path != NULL && socket_path[0] != '\0') {
        size_t len = strlen(socket_path);
        p->auto_discover = 0;
        if (len < sizeof(p->socket_path)) {
            memcpy(p->socket_path, socket_path, len + 1);
        }
    } else {
        p->auto_discover = 1;
    }
}

void fleetty_progress_close(fleetty_progress_t *p) {
    if (p == NULL) {
        return;
    }
    if (p->last_fd >= 0) {
        close(p->last_fd);
        p->last_fd = -1;
    }
}

static size_t fleetty_utf8_clamped_len(const char *s, size_t max_bytes) {
    size_t len = 0;
    while (len < max_bytes && s[len] != '\0') {
        len++;
    }
    if (len == max_bytes && s[len] != '\0') {
        while (len > 0 && (((unsigned char)s[len] & 0xC0U) == 0x80U)) {
            len--;
        }
    }
    return len;
}

static int fleetty_format_progress_json(
    char *out,
    size_t cap,
    uint64_t done,
    uint64_t total,
    const char *label,
    size_t *out_len
) {
    int n = snprintf(out, cap, "{\"v\":1,\"done\":%llu,\"total\":%llu",
                     (unsigned long long)done, (unsigned long long)total);
    if (n < 0 || (size_t)n + 3 > cap) {
        return -1;
    }
    size_t pos = (size_t)n;

    if (label != NULL && label[0] != '\0') {
        static const char prefix[] = ",\"label\":\"";
        size_t plen = sizeof(prefix) - 1;
        if (pos + plen >= cap) {
            return -1;
        }
        memcpy(out + pos, prefix, plen);
        pos += plen;

        size_t raw_len = fleetty_utf8_clamped_len(label, FLEETTY_LABEL_RAW_MAX);
        for (size_t i = 0; i < raw_len; i++) {
            unsigned char ch = (unsigned char)label[i];
            const char *esc = NULL;
            size_t esc_len = 0;
            char ctrl[7];
            switch (ch) {
            case '"':
                esc = "\\\"";
                esc_len = 2;
                break;
            case '\\':
                esc = "\\\\";
                esc_len = 2;
                break;
            case '\b':
                esc = "\\b";
                esc_len = 2;
                break;
            case '\f':
                esc = "\\f";
                esc_len = 2;
                break;
            case '\n':
                esc = "\\n";
                esc_len = 2;
                break;
            case '\r':
                esc = "\\r";
                esc_len = 2;
                break;
            case '\t':
                esc = "\\t";
                esc_len = 2;
                break;
            default:
                if (ch < 0x20U) {
                    (void)snprintf(ctrl, sizeof(ctrl), "\\u%04x", (unsigned int)ch);
                    esc = ctrl;
                    esc_len = 6;
                }
                break;
            }
            if (esc != NULL) {
                if (pos + esc_len + 3 >= cap) {
                    return -1;
                }
                memcpy(out + pos, esc, esc_len);
                pos += esc_len;
            } else {
                if (pos + 4 >= cap) {
                    return -1;
                }
                out[pos++] = (char)ch;
            }
        }
        out[pos++] = '"';
    }

    if (pos + 2 >= cap) {
        return -1;
    }
    out[pos++] = '}';
    out[pos++] = '\n';
    out[pos] = '\0';
    *out_len = pos;
    return 0;
}

static int fleetty_open_socket(void) {
#if defined(__linux__) && defined(SOCK_NONBLOCK) && defined(SOCK_CLOEXEC)
    int fd = socket(AF_UNIX, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (fd >= 0 || errno != EINVAL) {
        return fd;
    }
#endif
    int s = socket(AF_UNIX, SOCK_STREAM, 0);
    if (s < 0) {
        return -1;
    }
    int flags = fcntl(s, F_GETFL, 0);
    if (flags >= 0) {
        (void)fcntl(s, F_SETFL, flags | O_NONBLOCK);
    }
    (void)fcntl(s, F_SETFD, FD_CLOEXEC);
    return s;
}

static int fleetty_send_to_path(
    const char *path,
    const char *payload,
    size_t len,
    int *out_fd
) {
    if (path == NULL || path[0] == '\0') {
        return -1;
    }
    struct sockaddr_un addr;
    size_t plen = strlen(path);
    if (plen >= sizeof(addr.sun_path)) {
        return -1;
    }
    memset(&addr, 0, sizeof(addr));
    addr.sun_family = AF_UNIX;
    memcpy(addr.sun_path, path, plen + 1);

    int fd = fleetty_open_socket();
    if (fd < 0) {
        return -1;
    }
#ifdef SO_NOSIGPIPE
    int one = 1;
    (void)setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof(one));
#endif
    while (connect(fd, (const struct sockaddr *)&addr, sizeof(addr)) != 0) {
        if (errno == EINTR) {
            continue;
        }
        close(fd);
        return -1;
    }

    int send_flags = 0;
#ifdef MSG_NOSIGNAL
    send_flags = MSG_NOSIGNAL;
#endif
    size_t sent = 0;
    while (sent < len) {
        ssize_t n = send(fd, payload + sent, len - sent, send_flags);
        if (n > 0) {
            sent += (size_t)n;
            continue;
        }
        if (n < 0 && errno == EINTR) {
            continue;
        }
        close(fd);
        return -1;
    }

    *out_fd = fd;
    return 0;
}

static int fleetty_dispatch(
    fleetty_progress_t *p,
    const char *payload,
    size_t len,
    int wait_reply
) {
    /* Close the previous fire-and-forget socket after the server has read it. */
    fleetty_progress_close(p);

    int fd = -1;
    int rc = -1;
    if (!p->auto_discover) {
        rc = fleetty_send_to_path(p->socket_path, payload, len, &fd);
    } else {
        const char *env_path = getenv("FLEETTY_PROGRESS_SOCKET");
        if (env_path != NULL && env_path[0] != '\0') {
            rc = fleetty_send_to_path(env_path, payload, len, &fd);
        } else {
            char default_path[FLEETTY_PROGRESS_PATH_MAX];
            if (fleetty_progress_default_path(default_path, sizeof(default_path)) == 0) {
                rc = fleetty_send_to_path(default_path, payload, len, &fd);
            }
#if defined(__linux__)
            if (rc != 0 && geteuid() != 0) {
                rc = fleetty_send_to_path("/run/fleetty-progress-v1.sock", payload, len, &fd);
            }
#endif
        }
    }

    if (rc != 0) {
        return -1;
    }
    if (!wait_reply) {
        p->last_fd = fd;
        return 0;
    }

    /* Briefly drain the reply so clearing right before exit does not drop peer credentials. */
    struct pollfd pfd;
    pfd.fd = fd;
    pfd.events = POLLIN;
    pfd.revents = 0;
    int prc;
    do {
        prc = poll(&pfd, 1, 20);
    } while (prc < 0 && errno == EINTR);
    if (prc > 0 && (pfd.revents & POLLIN) != 0) {
        char discard[128];
        while (recv(fd, discard, sizeof(discard), 0) > 0) {
        }
    }
    close(fd);
    return 0;
}

static int fleetty_send_update(
    fleetty_progress_t *p,
    uint64_t done,
    uint64_t total,
    const char *label,
    int force
) {
    if (p == NULL || total == 0 || done > total) {
        return -1;
    }
    if (!force && p->min_interval_ns > 0) {
        uint64_t now = fleetty_monotonic_ns();
        if (p->last_sent_ns != 0 && now - p->last_sent_ns < p->min_interval_ns) {
            return 0;
        }
        p->last_sent_ns = now;
    } else {
        p->last_sent_ns = fleetty_monotonic_ns();
    }

    char payload[FLEETTY_PAYLOAD_CAP];
    size_t len = 0;
    if (fleetty_format_progress_json(payload, sizeof(payload), done, total, label, &len) != 0) {
        return -1;
    }
    return fleetty_dispatch(p, payload, len, 0) == 0 ? 1 : -1;
}

int fleetty_progress_report(
    fleetty_progress_t *p,
    uint64_t done,
    uint64_t total,
    const char *label
) {
    return fleetty_send_update(p, done, total, label, 0);
}

int fleetty_progress_report_force(
    fleetty_progress_t *p,
    uint64_t done,
    uint64_t total,
    const char *label
) {
    return fleetty_send_update(p, done, total, label, 1);
}

int fleetty_progress_clear(fleetty_progress_t *p) {
    if (p == NULL) {
        return -1;
    }
    static const char clear_msg[] = "{\"v\":1,\"clear\":true}\n";
    p->last_sent_ns = 0;
    return fleetty_dispatch(p, clear_msg, sizeof(clear_msg) - 1, 1);
}
