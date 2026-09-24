// Command harness is the Go port of the harness coding agent.
//
// This scaffold implements exactly one subcommand, "session inspect
// <path>", used to validate the phase-1 session JSONL store. Every other
// invocation keeps the prior stub behaviour: print a message to stderr and
// exit 2.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "session" && os.Args[2] == "inspect" {
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: harness session inspect <path>")
			os.Exit(2)
		}
		if err := sessionInspect(os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, "harness session inspect:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "harness: go port scaffold; not yet implemented")
	os.Exit(2)
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
