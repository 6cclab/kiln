package tools

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Discovery uses the snapshot's entry ceiling even before fingerprinting, so
// parsing a large tree cannot turn one Bash call into an unbounded scan.
type goDiscoveryScan struct {
	cwd, module string
	allowRead   func(string) bool
	entries     int
	bytes       int64
	seen        map[string]bool
	queue       []string
	directories []string
	listed      map[string][]string
}

func discoverGoLocalDependencies(cwd, selected string, recursive bool, allowRead func(string) bool) ([]string, error) {
	mod, err := readGoDiscoveryFile(cwd, "go.mod", allowRead)
	if err != nil {
		return nil, err
	}
	module, replacements, err := parseGoDiscoveryMod(string(mod), cwd)
	if err != nil {
		return nil, err
	}
	scan := &goDiscoveryScan{cwd: cwd, module: module, allowRead: allowRead, seen: map[string]bool{selected: true}, listed: make(map[string][]string)}
	if err := scan.selectedPackages(selected, recursive); err != nil {
		return nil, err
	}
	for len(scan.queue) > 0 {
		path := scan.queue[0]
		scan.queue = scan.queue[1:]
		if err := scan.packageImports(path); err != nil {
			return nil, err
		}
	}
	for _, replacement := range replacements {
		if !scan.seen[replacement] {
			scan.directories = append(scan.directories, replacement)
			scan.seen[replacement] = true
		}
	}
	return scan.directories, nil
}

func (s *goDiscoveryScan) selectedPackages(selected string, recursive bool) error {
	if !recursive {
		s.queue = append(s.queue, selected)
		return nil
	}
	if s.listed == nil {
		s.listed = make(map[string][]string)
	}
	var walk func(string) error
	walk = func(rel string) error {
		if !s.allowRead(rel) {
			return fmt.Errorf("discovery directory unavailable")
		}
		full := filepath.Join(s.cwd, rel)
		info, err := os.Lstat(full)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("discovery directory unavailable")
		}
		names, err := s.readNames(rel)
		if err != nil {
			return err
		}
		s.listed[rel] = names
		s.queue = append(s.queue, rel)
		for _, name := range names {
			if name == ".git" {
				continue
			}
			child := filepath.Join(rel, name)
			info, err := os.Lstat(filepath.Join(s.cwd, child))
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(selected)
}

func (s *goDiscoveryScan) packageImports(path string) error {
	if path != "." && !validRelativeVerificationPath(path) || !s.allowRead(path) {
		return fmt.Errorf("discovery package unavailable")
	}
	names, listed := s.listed[path]
	if !listed {
		var err error
		names, err = s.readNames(path)
		if err != nil {
			return err
		}
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		rel := filepath.Join(path, name)
		info, err := os.Lstat(filepath.Join(s.cwd, rel))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("discovery Go file is not regular")
		}
		contents, err := readGoDiscoveryFile(s.cwd, rel, s.allowRead)
		if err != nil {
			return err
		}
		s.bytes += int64(len(contents))
		if s.bytes > maxVerificationDirectoryBytes {
			return errVerificationBounds
		}
		file, err := parser.ParseFile(token.NewFileSet(), rel, contents, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			return err
		}
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if strings.HasPrefix(comment.Text, "//go:build") || strings.HasPrefix(comment.Text, "// +build") {
					return fmt.Errorf("build tags unsupported")
				}
			}
		}
		for _, imp := range file.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil || name == "C" {
				return fmt.Errorf("cgo or malformed import unsupported")
			}
			if name != s.module && !strings.HasPrefix(name, s.module+"/") {
				continue
			}
			dep := strings.TrimPrefix(name, s.module)
			dep = strings.TrimPrefix(dep, "/")
			if dep == "" {
				dep = "."
			}
			if dep != "." && !validRelativeVerificationPath(dep) {
				return fmt.Errorf("invalid in-module import")
			}
			if s.seen[dep] {
				continue
			}
			if _, err := os.Lstat(filepath.Join(s.cwd, dep)); err != nil {
				return err
			}
			s.seen[dep] = true
			s.directories = append(s.directories, dep)
			s.queue = append(s.queue, dep)
		}
	}
	return nil
}

func (s *goDiscoveryScan) readNames(rel string) ([]string, error) {
	remaining := maxVerificationDirectoryFiles - s.entries
	if remaining < 0 {
		return nil, errVerificationBounds
	}
	folder, err := os.Open(filepath.Join(s.cwd, rel))
	if err != nil {
		return nil, err
	}
	names, readErr := folder.Readdirnames(remaining + 1)
	closeErr := folder.Close()
	if readErr != nil && readErr != io.EOF {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(names) > remaining {
		return nil, errVerificationBounds
	}
	s.entries += len(names)
	slices.Sort(names)
	return names, nil
}

func readGoDiscoveryFile(cwd, rel string, allowRead func(string) bool) ([]byte, error) {
	if !validRelativeVerificationPath(rel) || !allowRead(rel) {
		return nil, fmt.Errorf("discovery file unavailable")
	}
	full := filepath.Join(cwd, rel)
	for cursor := full; cursor != cwd; cursor = filepath.Dir(cursor) {
		info, err := os.Lstat(cursor)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("discovery path unavailable")
		}
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("discovery file unavailable or too large")
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(contents) > 1<<20 {
		return nil, fmt.Errorf("discovery file unavailable or too large")
	}
	return contents, nil
}

func parseGoDiscoveryMod(body, cwd string) (string, []string, error) {
	var module string
	var replacements []string
	inReplace := false
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "//", 2)[0])
		if line == "" {
			continue
		}
		if line == "replace (" {
			inReplace = true
			continue
		}
		if inReplace && line == ")" {
			inReplace = false
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			module = strings.Trim(fields[1], `"`)
			continue
		}
		if !inReplace && fields[0] != "replace" {
			continue
		}
		if !inReplace {
			fields = fields[1:]
		}
		arrow := -1
		for i, field := range fields {
			if field == "=>" {
				arrow = i
				break
			}
		}
		if arrow < 1 || arrow+2 != len(fields) {
			return "", nil, fmt.Errorf("unsupported replace directive")
		}
		target := strings.Trim(fields[arrow+1], `"`)
		if !strings.HasPrefix(target, ".") && !filepath.IsAbs(target) {
			continue
		}
		root, err := filepath.Abs(cwd)
		if err != nil {
			return "", nil, err
		}
		clean := filepath.Clean(target)
		if !filepath.IsAbs(clean) {
			full := filepath.Join(root, clean)
			clean, err = filepath.Rel(root, full)
		}
		if err != nil || !validGoDiscoveryDirectoryPath(clean) {
			return "", nil, fmt.Errorf("local replace path unsupported")
		}
		replacements = append(replacements, clean)
	}
	if scanner.Err() != nil || module == "" || inReplace {
		return "", nil, fmt.Errorf("invalid module metadata")
	}
	return module, replacements, nil
}

func validGoExternalVerificationPath(path string) bool {
	if !filepath.IsAbs(path) && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return false
	}
	if len(path) > 4096 || filepath.Clean(path) != path {
		return false
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".git" {
			return false
		}
	}
	return true
}

func validGoDiscoveryDirectoryPath(path string) bool {
	return path == "." || validRelativeVerificationPath(path) || validGoExternalVerificationPath(path)
}
