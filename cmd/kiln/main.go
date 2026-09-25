// Command kiln is the Go port of the harness coding agent.
//
// --version/--help answer before anything else, matching cli.ts: they must
// work even when a project's configuration is broken. An unknown flag is
// reported and refused rather than silently ignored — cli.ts's own
// parseArgs collects rather than drops mistyped flags for the same reason.
// Everything else dispatches to internal/cli: the management subcommands
// (providers, models, login, logout, doctor, mcp), `session inspect`
// (unchanged from the phase-1 scaffold), and chat (`-p`/interactive) as the
// default.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/cli"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	// "eval" is dispatched before cli.Parse, not through it: it has its
	// own flag set (--kiln-bin, --scenarios, --models, ...) that cli.Parse
	// does not know and would otherwise reject as unknown flags.
	if len(argv) > 0 && argv[0] == "eval" {
		return evalCommand(context.Background(), argv[1:], os.Stdout, os.Stderr)
	}

	args := cli.Parse(argv)

	// These two answer before anything else, deliberately ahead of the
	// unknown-flag check: `harness --help --bogus` should still print help.
	if args.Version {
		return cli.VersionCmd(os.Stdout)
	}
	if args.Help {
		fmt.Fprintln(os.Stdout, cli.Help)
		return 0
	}
	if len(args.Unknown) > 0 {
		fmt.Fprintf(os.Stderr, "unknown flag(s): %s\n", strings.Join(args.Unknown, ", "))
		return 1
	}

	ctx := context.Background()

	switch args.Command {
	case "providers":
		return cli.Providers(ctx, os.Stdout, os.Stderr)
	case "models":
		providerID := ""
		if len(args.Positional) > 0 {
			providerID = args.Positional[0]
		}
		return cli.Models(ctx, os.Stdout, os.Stderr, providerID)
	case "login":
		if len(args.Positional) == 0 {
			fmt.Fprintln(os.Stderr, "usage: kiln login <provider>")
			return 1
		}
		return cli.LoginCmd(ctx, args.Positional[0], os.Stdin, os.Stdout, os.Stderr)
	case "logout":
		if len(args.Positional) == 0 {
			fmt.Fprintln(os.Stderr, "usage: kiln logout <provider>")
			return 1
		}
		return cli.LogoutCmd(ctx, args.Positional[0], os.Stdout, os.Stderr)
	case "doctor":
		return cli.Doctor(ctx, args, os.Stdout, os.Stderr)
	case "mcp":
		return cli.MCP(ctx, args, os.Stdout, os.Stderr)
	case "session":
		return sessionCommand(args.Positional)
	default:
		return cli.Run(ctx, args, os.Stdout, os.Stderr, os.Stdin)
	}
}

// sessionCommand implements `harness session inspect <path>`, kept
// verbatim from the phase-1 scaffold: it validates the session JSONL store
// directly, independent of the provider/agent stack this phase adds.
func sessionCommand(positional []string) int {
	if len(positional) != 2 || positional[0] != "inspect" {
		fmt.Fprintln(os.Stderr, "usage: kiln session inspect <path>")
		return 2
	}
	if err := sessionInspect(positional[1]); err != nil {
		fmt.Fprintln(os.Stderr, "kiln session inspect:", err)
		return 1
	}
	return 0
}

// inspectReport is the JSON shape "harness session inspect" prints.
type inspectReport struct {
	Header           session.Header                       `json:"header"`
	EntryCountByType map[string]int                       `json:"entryCountByType"`
	BranchTips       map[string]*string                   `json:"branchTips"`
	LaneConfigs      map[string]session.LaneConfiguration `json:"laneConfigs"`
	SeqRange         [2]int64                             `json:"seqRange"`
	UsageTotals      session.SessionStats                 `json:"usageTotals"`
}

func sessionInspect(path string) error {
	st, err := jsonl.Open(path, nil)
	if err != nil {
		return err
	}
	defer st.Close()

	entries := st.ScanEntries(session.EntryScan{})
	countByType := map[string]int{}
	var minSeq, maxSeq int64
	for i, e := range entries {
		countByType[string(e.Type)]++
		if i == 0 || e.Seq < minSeq {
			minSeq = e.Seq
		}
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}

	branchTips := map[string]*string{}
	for _, v := range st.ScanValues(session.NamespaceBranchTip, "") {
		tip, err := session.GetTypedValue[*string](v.Value)
		if err != nil {
			return fmt.Errorf("decode branch tip %s: %w", v.Key, err)
		}
		branchTips[v.Key] = tip
	}

	laneConfigs := map[string]session.LaneConfiguration{}
	for _, v := range st.ScanValues(session.NamespaceLaneConfig, "") {
		cfg, err := session.GetTypedValue[session.LaneConfiguration](v.Value)
		if err != nil {
			return fmt.Errorf("decode lane config %s: %w", v.Key, err)
		}
		laneConfigs[v.Key] = cfg
	}

	report := inspectReport{
		Header:           st.Header(),
		EntryCountByType: countByType,
		BranchTips:       branchTips,
		LaneConfigs:      laneConfigs,
		SeqRange:         [2]int64{minSeq, maxSeq},
		UsageTotals:      st.GetStats(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
