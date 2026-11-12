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
	cwd         string
	modules     []goDiscoveryModule
	allowRead   func(string) bool
	entries     int
	bytes       int64
	seen        map[string]bool
	queue       []string
	directories []string
	listed      map[string][]string
}

// goDiscoveryModule maps a module path to its directory relative to the root
// (only modules inside the root are traced at package level).
type goDiscoveryModule struct{ path, dir string }

func discoverGoLocalDependencies(cwd, selected string, recursive bool, allowRead func(string) bool) ([]string, error) {
	mod, err := readGoDiscoveryFile(cwd, "go.mod", allowRead)
	if err != nil {
		return nil, err
	}
	rootMod, err := parseGoDiscoveryDirectives(string(mod), cwd, true)
	if err != nil {
		return nil, err
	}
	replacements := rootMod.replaces
	scan := &goDiscoveryScan{cwd: cwd, modules: []goDiscoveryModule{{rootMod.module, "."}}, allowRead: allowRead, seen: map[string]bool{selected: true}, listed: make(map[string][]string)}
	workRoots, workReplaces, err := scan.workspaceModules(selected)
	if err != nil {
		return nil, err
	}
	replacements = append(replacements, workReplaces...)
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
	// A workspace module root is snapshotted whole, so packages traced inside
	// it would only repeat its entries against the shared limits.
	kept := scan.directories[:0]
	for _, dir := range scan.directories {
		nested := false
		for _, root := range workRoots {
			if goDiscoveryWithin(dir, root) {
				nested = true
			}
		}
		if !nested {
			kept = append(kept, dir)
		}
	}
	scan.directories = append(kept, workRoots...)
	for _, replacement := range replacements {
		if !scan.seen[replacement] {
			scan.directories = append(scan.directories, replacement)
			scan.seen[replacement] = true
		}
	}
	return scan.directories, nil
}

func goDiscoveryWithin(dir, root string) bool {
	if root == "." {
		return !filepath.IsAbs(dir) && dir != ".." && !strings.HasPrefix(dir, ".."+string(filepath.Separator))
	}
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}

// workspaceModules reads go.work (when present) and registers each local used
// module inside the root for import tracing. It returns the used module roots
// to snapshot whole (except those inside the selected tree) and go.work/go.mod
// replacement targets. Used modules outside the root are snapshotted but their
// imports are not traced.
func (s *goDiscoveryScan) workspaceModules(selected string) (roots, replaces []string, err error) {
	if _, statErr := os.Lstat(filepath.Join(s.cwd, "go.work")); os.IsNotExist(statErr) {
		return nil, nil, nil
	}
	body, err := readGoDiscoveryFile(s.cwd, "go.work", s.allowRead)
	if err != nil {
		return nil, nil, err
	}
	work, err := parseGoDiscoveryDirectives(string(body), s.cwd, false)
	if err != nil {
		return nil, nil, err
	}
	replaces = work.replaces
	for _, dir := range work.uses {
		if dir == "." {
			continue
		}
		if !goDiscoveryWithin(dir, selected) && !slices.Contains(roots, dir) {
			roots = append(roots, dir)
		}
		if err := s.modulesFromUse(dir, &replaces); err != nil {
			return nil, nil, err
		}
	}
	return roots, replaces, nil
}

func (s *goDiscoveryScan) modulesFromUse(dir string, replaces *[]string) error {
	if validGoExternalVerificationPath(dir) {
		return nil
	}
	body, err := readGoDiscoveryFile(s.cwd, filepath.Join(dir, "go.mod"), s.allowRead)
	if err != nil {
		return err
	}
	mod, err := parseGoDiscoveryDirectives(string(body), filepath.Join(s.cwd, dir), true)
	if err != nil {
		return err
	}
	s.modules = append(s.modules, goDiscoveryModule{mod.module, dir})
	for _, replacement := range mod.replaces {
		if !filepath.IsAbs(replacement) {
			replacement = filepath.Join(dir, replacement)
		}
		if !validGoDiscoveryDirectoryPath(replacement) {
			return fmt.Errorf("local replace path unsupported")
		}
		*replaces = append(*replaces, replacement)
	}
	return nil
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
		file, err := parser.ParseFile(token.NewFileSet(), rel, contents, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return fmt.Errorf("malformed import unsupported")
			}
			dep, ok := s.resolveImport(name)
			if !ok {
				continue
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

// resolveImport maps an import path to a package directory inside the root
// using the longest matching module path (root module or traced workspace
// module). "C" and everything else outside those modules are not traced.
func (s *goDiscoveryScan) resolveImport(name string) (string, bool) {
	best := -1
	for i, mod := range s.modules {
		if name != mod.path && !strings.HasPrefix(name, mod.path+"/") {
			continue
		}
		if best < 0 || len(mod.path) > len(s.modules[best].path) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	mod := s.modules[best]
	rest := strings.TrimPrefix(strings.TrimPrefix(name, mod.path), "/")
	return filepath.Join(mod.dir, filepath.FromSlash(rest)), true
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

type goDiscoveryDirectives struct {
	module         string
	replaces, uses []string
}

// parseGoDiscoveryDirectives reads the module, local replace and (go.work) use
// directives of a go.mod or go.work body. Paths are relative to base when
// relative, else absolute; base itself is cwd or a module directory.
func parseGoDiscoveryDirectives(body, base string, requireModule bool) (goDiscoveryDirectives, error) {
	var out goDiscoveryDirectives
	block := ""
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "//", 2)[0])
		if line == "" {
			continue
		}
		if block != "" && line == ")" {
			block = ""
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "(" && (fields[0] == "replace" || fields[0] == "use") {
			block = fields[0]
			continue
		}
		if len(fields) == 2 && fields[0] == "module" {
			out.module = strings.Trim(fields[1], `"`)
			continue
		}
		kind := block
		if kind == "" {
			kind = fields[0]
			fields = fields[1:]
		}
		switch kind {
		case "use":
			if len(fields) != 1 {
				return out, fmt.Errorf("unsupported use directive")
			}
			dir, err := goDiscoveryLocalPath(strings.Trim(fields[0], `"`), base)
			if err != nil {
				return out, err
			}
			out.uses = append(out.uses, dir)
		case "replace":
			arrow := -1
			for i, field := range fields {
				if field == "=>" {
					arrow = i
					break
				}
			}
			if arrow < 1 || arrow+2 != len(fields) {
				return out, fmt.Errorf("unsupported replace directive")
			}
			target := strings.Trim(fields[arrow+1], `"`)
			if !strings.HasPrefix(target, ".") && !filepath.IsAbs(target) {
				continue
			}
			clean, err := goDiscoveryLocalPath(target, base)
			if err != nil {
				return out, err
			}
			out.replaces = append(out.replaces, clean)
		}
	}
	if scanner.Err() != nil || requireModule && out.module == "" || block != "" {
		return out, fmt.Errorf("invalid module metadata")
	}
	return out, nil
}

func goDiscoveryLocalPath(target, base string) (string, error) {
	root, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(target)
	if !filepath.IsAbs(clean) {
		clean, err = filepath.Rel(root, filepath.Join(root, clean))
	}
	if err != nil || !validGoDiscoveryDirectoryPath(clean) {
		return "", fmt.Errorf("local replace path unsupported")
	}
	return clean, nil
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
