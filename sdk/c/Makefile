CC ?= cc
CFLAGS ?= -std=c99 -O2 -Wall -Wextra -Werror
AR ?= ar

.PHONY: all test clean

all: libfleetty_progress.a example

fleetty_progress.o: fleetty_progress.c fleetty_progress.h
	$(CC) $(CFLAGS) -c fleetty_progress.c -o $@

libfleetty_progress.a: fleetty_progress.o
	$(AR) rcs $@ $<

example: example.c fleetty_progress.c fleetty_progress.h
	$(CC) $(CFLAGS) example.c fleetty_progress.c -o $@

fleetty_progress_test: fleetty_progress_test.c fleetty_progress.c fleetty_progress.h
	$(CC) $(CFLAGS) fleetty_progress_test.c fleetty_progress.c -o $@

test: fleetty_progress_test
	./fleetty_progress_test

clean:
	rm -f fleetty_progress.o libfleetty_progress.a example fleetty_progress_test
