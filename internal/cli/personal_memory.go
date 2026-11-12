package cli

import (
	"context"
	"fmt"
	"github.com/andrepato/harness/internal/diag"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/andrepato/harness/internal/gitfiles"
	"github.com/andrepato/harness/internal/memory"
)

func memoryScope(cwd string) string {
	scope := cwd
	if repo, ok := gitfiles.Find(cwd); ok {
		scope = repo.CommonDir
	}
	if canonical, err := filepath.EvalSymlinks(scope); err == nil {
		scope = canonical
	}
	return scope
}

func memoryProjectRoot(cwd string) string {
	root := cwd
	if repo, ok := gitfiles.Find(cwd); ok {
		root = repo.WorkTree
	}
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	return root
}

// PersonalMemory is a local management command; it does not start a model.
func PersonalMemory(ctx context.Context, args Args, out, stderr io.Writer) int {
	usage := "usage: kiln memory [global] export | forget <key> | restore <id> | remember <key> <text> [source-files...]"
	positional := args.Positional
	global := false
	if len(positional) > 0 && positional[0] == "global" {
		global = true
		positional = positional[1:]
	}
	if len(positional) == 0 {
		writeDiagnostic(stderr, usage)
		return 2
	}
	action := positional[0]
	if (action == "export" && len(positional) != 1) || ((action == "forget" || action == "restore") && len(positional) != 2) || (action == "remember" && len(positional) < 3) || (global && action == "remember" && len(positional) != 3) || (action != "export" && action != "forget" && action != "restore" && action != "remember") {
		writeDiagnostic(stderr, usage)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	s, err := memory.Open(filepath.Join(home, ".kiln", "memory.db"))
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	defer diag.Close(s)
	scope := memoryScope(cwd)
	if global {
		scope = memory.GlobalPreferencesScope
	}
	switch action {
	case "remember":
		var sources []memory.Source
		paths := append([]string(nil), positional[3:]...)
		for i, path := range paths {
			if !filepath.IsAbs(path) {
				paths[i] = filepath.Join(cwd, path)
			}
		}
		sources, err = memory.CaptureSources(memoryProjectRoot(cwd), paths)
		if err == nil {
			var id int64
			kind := "observation"
			if global {
				kind = "preference"
			}
			id, err = s.Put(ctx, memory.Record{Scope: scope, Key: positional[1], Kind: kind, Text: positional[2], Evidence: "Explicit user memory command", Sources: sources})
			if err == nil {
				_, err = fmt.Fprintln(out, "Stored memory", id)
			}
		}
	case "export":
		var data []byte
		data, err = s.Export(ctx, scope)
		if err == nil {
			_, err = fmt.Fprintln(out, string(data))
		}
	case "forget":
		err = s.Forget(ctx, scope, positional[1])
		if err == nil {
			_, err = fmt.Fprintln(out, "Forgot all versions of", positional[1])
		}
	case "restore":
		var id int64
		id, err = strconv.ParseInt(positional[1], 10, 64)
		if err == nil {
			var restored int64
			restored, err = s.Restore(ctx, scope, id)
			if err == nil {
				_, err = fmt.Fprintln(out, "Restored as memory", restored)
			}
		}
	}
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	return 0
}
