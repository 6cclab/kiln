package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/andrepato/harness/internal/agent"
)

// Worker inspects or explicitly applies a retained isolated-worker artifact.
// Applying is a user-invoked management action, never an automatic tool effect.
func Worker(ctx context.Context, args Args, out, stderr io.Writer) int {
	if len(args.Positional) >= 2 && (args.Positional[0] == "inspect-batch" || args.Positional[0] == "apply-batch") {
		artifacts := make([]agent.WorkerArtifact, 0, len(args.Positional)-1)
		if len(args.Positional)-1 > 16 {
			writeDiagnostic(stderr, "worker batch exceeds 16 artifacts")
			return 2
		}
		for _, manifest := range args.Positional[1:] {
			artifact, err := agent.LoadWorkerArtifact(manifest)
			if err != nil {
				writeDiagnostic(stderr, err)
				return 1
			}
			artifacts = append(artifacts, artifact)
		}
		cwd, err := os.Getwd()
		if err != nil {
			writeDiagnostic(stderr, err)
			return 1
		}
		if args.Positional[0] == "inspect-batch" {
			var inspection agent.WorkerBatchInspection
			inspection, err = agent.InspectWorkerArtifactBatch(ctx, cwd, artifacts)
			if err == nil {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				err = encoder.Encode(inspection)
			}
		} else {
			err = agent.ApplyWorkerArtifactBatch(ctx, cwd, artifacts)
			if err == nil {
				_, err = fmt.Fprintln(out, "Applied worker batch. Review the working tree and run verification before committing.")
			}
		}
		if err != nil {
			writeDiagnostic(stderr, err)
			return 1
		}
		return 0
	}
	if len(args.Positional) != 2 || (args.Positional[0] != "apply" && args.Positional[0] != "inspect") {
		writeDiagnostic(stderr, "usage: kiln worker inspect|apply <artifact.json> | inspect-batch|apply-batch <artifact.json>...")
		return 2
	}
	artifact, err := agent.LoadWorkerArtifact(args.Positional[1])
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	if args.Positional[0] == "inspect" {
		_, err = agent.InspectWorkerArtifactBatch(ctx, cwd, []agent.WorkerArtifact{artifact})
		if err == nil {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			err = encoder.Encode(artifact)
		}
	} else {
		err = agent.ApplyWorkerArtifact(ctx, cwd, artifact)
		if err == nil {
			_, err = fmt.Fprintln(out, "Applied worker patch. Review the working tree and run verification before committing.")
		}
	}
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	return 0
}
