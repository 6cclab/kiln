// Command covercheck parses a Go coverprofile (as produced by
// `go test -coverprofile=...`), aggregates statement coverage per package,
// and optionally enforces a per-package floor file.
//
// Usage:
//
//	covercheck -profile coverage.out [-report] [-floors .coverage-floors]
//
// -report prints the per-package coverage table and exits 0.
//
// -floors compares each package listed in the floors file against its
// measured coverage and exits 1 if any listed package falls below its
// floor. Packages not listed in the floors file are printed but never
// cause a failing exit: the floor file is an opt-in ratchet, not a
// blanket requirement.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// modulePrefix is stripped from coverprofile file paths to produce a
// module-relative package path (e.g. "internal/tool" rather than
// "github.com/andrepato/harness/internal/tool").
const modulePrefix = "github.com/andrepato/harness/"

// pkgStats holds aggregated statement counts for one package.
type pkgStats struct {
	total   int
	covered int
}

func (p pkgStats) percent() float64 {
	if p.total == 0 {
		return 0
	}
	return 100 * float64(p.covered) / float64(p.total)
}

// parseProfile reads a Go coverprofile from r and aggregates statement
// coverage per package. The first line ("mode: ...") is skipped. Each
// subsequent line has the form:
//
//	file:startline.col,endline.col numstmt count
func parseProfile(r io.Reader) (map[string]*pkgStats, error) {
	pkgs := make(map[string]*pkgStats)
	sc := bufio.NewScanner(r)
	// Coverage lines can be long for files with many statements; grow the
	// scanner buffer defensively.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	first := true
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if first {
			first = false
			if strings.HasPrefix(line, "mode:") {
				continue
			}
			// No mode line; fall through and try to parse this line too.
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("line %d: expected 3 fields, got %d: %q", lineNo, len(fields), line)
		}
		location, numStmtStr, countStr := fields[0], fields[1], fields[2]

		colon := strings.Index(location, ":")
		if colon < 0 {
			return nil, fmt.Errorf("line %d: no ':' in location %q", lineNo, location)
		}
		file := location[:colon]

		numStmt, err := strconv.Atoi(numStmtStr)
		if err != nil {
			return nil, fmt.Errorf("line %d: bad numstmt %q: %w", lineNo, numStmtStr, err)
		}
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return nil, fmt.Errorf("line %d: bad count %q: %w", lineNo, countStr, err)
		}

		pkg := packageOf(file)
		st := pkgs[pkg]
		if st == nil {
			st = &pkgStats{}
			pkgs[pkg] = st
		}
		st.total += numStmt
		if count > 0 {
			st.covered += numStmt
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return pkgs, nil
}

// packageOf returns the module-relative package directory for a
// coverprofile file path, e.g.
// "github.com/andrepato/harness/internal/tool/bash.go" -> "internal/tool".
func packageOf(file string) string {
	rel := strings.TrimPrefix(file, modulePrefix)
	dir := path.Dir(rel)
	if dir == "" {
		return "."
	}
	return dir
}

// parseFloors reads a floors file of "<package>\t<floor>" lines. Blank
// lines and lines starting with '#' are ignored.
func parseFloors(r io.Reader) (map[string]float64, error) {
	floors := make(map[string]float64)
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("line %d: expected \"<package>\\t<floor>\", got %q", lineNo, line)
		}
		pkg := fields[0]
		floor, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: bad floor %q: %w", lineNo, fields[len(fields)-1], err)
		}
		floors[pkg] = floor
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return floors, nil
}

// sortedPackages returns pkgs' keys sorted by coverage percentage
// ascending (worst first), then alphabetically for ties.
func sortedPackages(pkgs map[string]*pkgStats) []string {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		pi, pj := pkgs[names[i]].percent(), pkgs[names[j]].percent()
		if pi != pj {
			return pi < pj
		}
		return names[i] < names[j]
	})
	return names
}

// writeTable prints the per-package coverage table to w.
func writeTable(w io.Writer, pkgs map[string]*pkgStats) {
	names := sortedPackages(pkgs)
	var totalStmt, totalCovered int
	fmt.Fprintf(w, "%-50s %10s %10s\n", "PACKAGE", "COVERAGE", "STATEMENTS")
	for _, name := range names {
		st := pkgs[name]
		totalStmt += st.total
		totalCovered += st.covered
		fmt.Fprintf(w, "%-50s %9.1f%% %5d/%d\n", name, st.percent(), st.covered, st.total)
	}
	var totalPct float64
	if totalStmt > 0 {
		totalPct = 100 * float64(totalCovered) / float64(totalStmt)
	}
	fmt.Fprintf(w, "%-50s %9.1f%% %5d/%d\n", "TOTAL", totalPct, totalCovered, totalStmt)
}

// checkFloors compares pkgs against floors, writing one line per package
// listed in floors to w, and returns the names of packages that fell
// below their floor. Packages not listed in floors are not reported here.
func checkFloors(w io.Writer, pkgs map[string]*pkgStats, floors map[string]float64) []string {
	names := make([]string, 0, len(floors))
	for name := range floors {
		names = append(names, name)
	}
	sort.Strings(names)

	var failing []string
	for _, name := range names {
		floor := floors[name]
		st := pkgs[name]
		if st == nil {
			st = &pkgStats{}
		}
		got := st.percent()
		if got+1e-9 < floor {
			fmt.Fprintf(w, "FAIL %-50s %9.2f%% < floor %.2f%%\n", name, got, floor)
			failing = append(failing, name)
		}
	}
	return failing
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("covercheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profilePath := fs.String("profile", "coverage.out", "path to a go coverprofile")
	floorsPath := fs.String("floors", "", "path to a .coverage-floors file (per-package floor enforcement)")
	report := fs.Bool("report", false, "print the coverage table only, skip floor enforcement")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	f, err := os.Open(*profilePath)
	if err != nil {
		fmt.Fprintf(stderr, "covercheck: %v\n", err)
		return 2
	}
	defer f.Close()

	pkgs, err := parseProfile(f)
	if err != nil {
		fmt.Fprintf(stderr, "covercheck: parsing %s: %v\n", *profilePath, err)
		return 2
	}

	writeTable(stdout, pkgs)

	if *report || *floorsPath == "" {
		return 0
	}

	ff, err := os.Open(*floorsPath)
	if err != nil {
		fmt.Fprintf(stderr, "covercheck: %v\n", err)
		return 2
	}
	defer ff.Close()

	floors, err := parseFloors(ff)
	if err != nil {
		fmt.Fprintf(stderr, "covercheck: parsing %s: %v\n", *floorsPath, err)
		return 2
	}

	failing := checkFloors(stdout, pkgs, floors)
	if len(failing) > 0 {
		fmt.Fprintf(stderr, "covercheck: %d package(s) below their coverage floor\n", len(failing))
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
