// Command harness-drive is a long-running process that drives a real
// harness binary through a real PTY, using internal/testkit/screen, and
// exposes it over a line protocol on stdin/stdout. It exists so a developer
// or an agent can drive the TUI end to end without a human at a keyboard.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

type envFlags []string

func (e *envFlags) String() string { return strings.Join(*e, ",") }
func (e *envFlags) Set(v string) error {
	*e = append(*e, v)
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout))
}

func run(args []string, in *os.File, out *os.File) int {
	fs := flag.NewFlagSet("harness-drive", flag.ContinueOnError)
	bin := fs.String("bin", "./bin/harness", "path to the harness binary to drive")
	cols := fs.Int("cols", 80, "terminal width")
	rows := fs.Int("rows", 24, "terminal height")
	record := fs.String("record", "", "tee raw PTY bytes to this file")
	var envs envFlags
	fs.Var(&envs, "env", "KEY=VAL environment variable (repeatable)")

	// Split off passthrough args after "--".
	flagArgs, passthrough := args, []string(nil)
	for i, a := range args {
		if a == "--" {
			flagArgs, passthrough = args[:i], args[i+1:]
			break
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintln(out, "error:", err)
		return 2
	}

	opts := []screen.Option{}
	for _, kv := range envs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(out, "error: invalid --env %q, expected KEY=VAL\n", kv)
			return 2
		}
		opts = append(opts, screen.WithEnv(k, v))
	}
	if *record != "" {
		opts = append(opts, screen.WithRecord(*record))
	}

	s, err := screen.StartDetached(*bin, passthrough, *cols, *rows, opts...)
	if err != nil {
		fmt.Fprintln(out, "error:", err)
		return 1
	}
	defer s.Close()

	d := &driver{screen: s, out: out, cols: *cols, rows: *rows}
	d.loop(in)
	return d.exitCode
}

type driver struct {
	screen   *screen.Screen
	out      *os.File
	cols     int
	rows     int
	exitCode int
	done     bool
}

func (d *driver) loop(in *os.File) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for !d.done && scanner.Scan() {
		line := scanner.Text()
		d.dispatch(line)
	}
}

func (d *driver) dispatch(line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		fmt.Fprintln(d.out, "ok")
		fmt.Fprintln(d.out, ".")
		return
	}

	cmd := strings.ToUpper(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))

	var err error
	switch cmd {
	case "SEND":
		d.screen.Send(rest)
	case "KEY":
		names := strings.Fields(rest)
		if len(names) == 0 {
			err = fmt.Errorf("KEY requires at least one key name")
		} else {
			d.screen.SendKey(names...)
		}
	case "WAIT":
		err = d.cmdWait(rest)
	case "SCREEN":
		err = d.cmdScreen(rest)
	case "RESIZE":
		err = d.cmdResize(rest)
	case "SLEEP":
		err = d.cmdSleep(rest)
	case "EXIT":
		err = d.cmdExit()
	case "KILL":
		d.screen.Close()
		d.done = true
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}

	if err != nil {
		fmt.Fprintf(d.out, "error: %v\n", err)
	} else {
		fmt.Fprintln(d.out, "ok")
	}
	fmt.Fprintln(d.out, ".")
}

// cmdWait handles "WAIT <text>" and "WAIT <timeout> <text>", e.g.
// "WAIT 5s some text". The first field is treated as a timeout if it parses
// as a Go duration; otherwise the whole remainder is the pattern with the
// default timeout. A pattern of the form "/regexp/" is compiled as a
// *regexp.Regexp; otherwise it is matched as a substring.
func (d *driver) cmdWait(rest string) error {
	timeout := screen.DefaultTimeout
	pattern := rest
	if fields := strings.Fields(rest); len(fields) > 1 {
		if dur, perr := time.ParseDuration(fields[0]); perr == nil {
			timeout = dur
			pattern = strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
		}
	}
	if pattern == "" {
		return fmt.Errorf("WAIT requires a pattern")
	}

	var pat any = pattern
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) >= 2 {
		re, rerr := regexp.Compile(pattern[1 : len(pattern)-1])
		if rerr != nil {
			return fmt.Errorf("invalid regexp %q: %w", pattern, rerr)
		}
		pat = re
	}
	return d.screen.WaitFor(pat, timeout)
}

func (d *driver) cmdScreen(rest string) error {
	asJSON := strings.TrimSpace(rest) == "--json"
	rows := d.screen.Viewport()
	cursorRow := d.screen.CursorRow()
	occupied := d.screen.OccupiedHeight()

	if asJSON {
		payload := struct {
			Rows           []string `json:"rows"`
			CursorRow      int      `json:"cursorRow"`
			OccupiedHeight int      `json:"occupiedHeight"`
			Cols           int      `json:"cols"`
			RowsCount      int      `json:"rowsCount"`
		}{
			Rows:           rows,
			CursorRow:      cursorRow,
			OccupiedHeight: occupied,
			Cols:           d.cols,
			RowsCount:      len(rows),
		}
		enc, merr := json.Marshal(payload)
		if merr != nil {
			return merr
		}
		fmt.Fprintln(d.out, string(enc))
		return nil
	}

	fmt.Fprintln(d.out, ruler(d.cols))
	for _, r := range rows {
		fmt.Fprintln(d.out, r)
	}
	fmt.Fprintf(d.out, "-- cursor: row %d, occupied: %d, size: %dx%d --\n", cursorRow, occupied, d.cols, len(rows))
	return nil
}

// ruler renders a column ruler like "0----5----10---15---20", with the
// decimal column number written at every multiple of 5 and "-" filling the
// rest.
func ruler(cols int) string {
	out := make([]byte, cols)
	for i := range out {
		out[i] = '-'
	}
	for i := 0; i < cols; i += 5 {
		label := strconv.Itoa(i)
		for j := 0; j < len(label) && i+j < cols; j++ {
			out[i+j] = label[j]
		}
	}
	return string(out)
}

func (d *driver) cmdResize(rest string) error {
	fields := strings.Fields(rest)
	if len(fields) != 2 {
		return fmt.Errorf("RESIZE requires <cols> <rows>")
	}
	cols, err := strconv.Atoi(fields[0])
	if err != nil {
		return fmt.Errorf("invalid cols %q: %w", fields[0], err)
	}
	rows, err := strconv.Atoi(fields[1])
	if err != nil {
		return fmt.Errorf("invalid rows %q: %w", fields[1], err)
	}
	d.screen.Resize(cols, rows)
	d.cols, d.rows = cols, rows
	return nil
}

func (d *driver) cmdSleep(rest string) error {
	dur, err := time.ParseDuration(strings.TrimSpace(rest))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", rest, err)
	}
	time.Sleep(dur)
	return nil
}

func (d *driver) cmdExit() error {
	code, err := d.screen.Exit()
	d.done = true
	d.exitCode = code
	if err != nil {
		return err
	}
	fmt.Fprintf(d.out, "exit code: %d\n", code)
	return nil
}
