package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"time"
)

func runProgressCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && args[0] == "--paths" {
		fmt.Fprintln(stdout, progressSocketPath(os.Geteuid()))
		if os.Geteuid() != 0 && runtime.GOOS == "linux" {
			fmt.Fprintln(stdout, progressSocketPath(0))
		}
		return nil
	}
	request, err := parseProgressFileCommand(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 || runtime.GOOS != "linux" {
		return errors.New("progress file commands require root on Linux")
	}
	return sendProgressFileCommand(progressSocketPath(0), request, stdout)
}

func parseProgressFileCommand(args []string, stderr io.Writer) (progressFileRequest, error) {
	const usage = "use fleetty progress --paths, attach-file, list-files or detach-file"
	if len(args) == 0 {
		return progressFileRequest{}, errors.New(usage)
	}
	req := progressFileRequest{Version: 1, Action: args[0]}
	flags := flag.NewFlagSet("progress "+req.Action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	if req.Action == "attach-file" || req.Action == "detach-file" {
		flags.IntVar(&req.PID, "pid", 0, "target PID")
		flags.Uint64Var(&req.Start, "start-ticks", 0, "target process start time (Linux /proc/PID/stat field 22)")
	}
	var total uint64
	if req.Action == "attach-file" {
		flags.StringVar(&req.Path, "path", "", "absolute JSON file path as seen by the target process")
		flags.StringVar(&req.DoneKey, "done-key", "", "top-level JSON key for the completed count")
		flags.StringVar(&req.TotalKey, "total-key", "", "top-level JSON key for the total count")
		flags.Uint64Var(&total, "total", 0, "fixed total count, instead of --total-key")
		flags.StringVar(&req.Label, "label", "", "public phase label")
	}
	if req.Action == "--help" || req.Action == "-h" || req.Action == "-help" {
		fmt.Fprintln(stderr, usage)
		return progressFileRequest{}, flag.ErrHelp
	}
	if err := flags.Parse(args[1:]); err != nil {
		return progressFileRequest{}, err
	}
	if flags.NArg() != 0 {
		return progressFileRequest{}, errors.New("unexpected positional argument")
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "total" {
			req.Total = &total
		}
	})
	return req, req.validate()
}

func sendProgressFileCommand(path string, request progressFileRequest, stdout io.Writer) error {
	line, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(line)+1 > processProgressLimit {
		return errors.New("progress file request exceeds 4096 bytes")
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return err
	}
	var reply progressReply
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if request.Action == "list-files" {
		if reply.Files == nil {
			return errors.New("progress file list is missing from the receiver reply")
		}
		return json.NewEncoder(stdout).Encode(*reply.Files)
	}
	return nil
}
