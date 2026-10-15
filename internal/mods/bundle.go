package mods

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// bundleVersion changes whenever the build settings below change, so a cache
// written with other settings is never reused.
const bundleVersion = "kiln-mods-1/esbuild-0.28.2/es2017-cjs-__h"

// jsxTsconfig replaces the plugin's own tsconfig.json: a mod's JSX always
// compiles against the engine's factory, as Claude Code prepends its pragma,
// and a tsconfig that extends a file the plugin does not ship (squad's points
// at .claude-plugin/types/) is never read.
const jsxTsconfig = `{"compilerOptions":{"jsx":"react","jsxFactory":"__h","jsxFragmentFactory":"__frag"}}`

// resolveMarker tags the esbuild resolve the guard makes itself, so the
// guard's own OnResolve callback lets it through instead of recursing.
type resolveMarker struct{}

// bundle compiles a hooks module and everything it imports into one
// CommonJS script that require()s only "claude-code". entry must be inside
// root, and so must every file it imports (symlinks are followed before the
// check): a module may not reach outside its plugin.
//
// With cacheDir set, the script is cached under it, keyed by the entry's
// path and checked against the content hash of every input file.
func bundle(root, entry, cacheDir string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("plugin root: %w", err)
	}
	realEntry, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return "", fmt.Errorf("hooks module: %w", err)
	}
	if !within(realRoot, realEntry) {
		return "", fmt.Errorf("hooks module %s is outside the plugin root %s", entry, root)
	}

	cachePath := ""
	if cacheDir != "" {
		sum := sha256.Sum256([]byte(bundleVersion + "\x00" + realRoot + "\x00" + realEntry))
		cachePath = filepath.Join(cacheDir, hex.EncodeToString(sum[:12])+".json")
		if code, ok := readBundleCache(cachePath, realRoot); ok {
			return code, nil
		}
	}

	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{realEntry},
		AbsWorkingDir:     realRoot,
		Bundle:            true,
		Write:             false,
		Metafile:          cachePath != "",
		Format:            api.FormatCommonJS,
		Target:            api.ES2017,
		Platform:          api.PlatformNeutral,
		MainFields:        []string{"module", "main"},
		External:          []string{"claude-code"},
		JSX:               api.JSXTransform,
		JSXFactory:        "__h",
		JSXFragment:       "__frag",
		TsconfigRaw:       jsxTsconfig,
		LogLevel:          api.LogLevelSilent,
		Plugins:           []api.Plugin{rootGuard(realRoot)},
		Charset:           api.CharsetUTF8,
		LegalComments:     api.LegalCommentsNone,
		ResolveExtensions: []string{".tsx", ".ts", ".jsx", ".js", ".mjs", ".cjs", ".mts", ".cts", ".json"},
	})
	if len(result.Errors) > 0 {
		return "", buildError(result.Errors)
	}
	if len(result.OutputFiles) != 1 {
		return "", fmt.Errorf("esbuild produced %d files, want 1", len(result.OutputFiles))
	}
	code := string(result.OutputFiles[0].Contents)
	if cachePath != "" {
		writeBundleCache(cachePath, realRoot, result.Metafile, code)
	}
	return code, nil
}

// rootGuard refuses any import that resolves outside the plugin root, and any
// external import other than "claude-code".
func rootGuard(root string) api.Plugin {
	return api.Plugin{
		Name: "kiln-plugin-root",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if _, mine := args.PluginData.(resolveMarker); mine {
					return api.OnResolveResult{}, nil
				}
				if args.Path == "claude-code" {
					return api.OnResolveResult{Path: "claude-code", External: true}, nil
				}
				if args.Kind == api.ResolveEntryPoint {
					return api.OnResolveResult{}, nil
				}
				r := b.Resolve(args.Path, api.ResolveOptions{
					Importer:   args.Importer,
					Namespace:  args.Namespace,
					ResolveDir: args.ResolveDir,
					Kind:       args.Kind,
					PluginData: resolveMarker{},
				})
				if len(r.Errors) > 0 {
					return api.OnResolveResult{}, buildError(r.Errors)
				}
				if r.External || r.Namespace != "file" {
					return api.OnResolveResult{}, fmt.Errorf("import %q is not a file in the plugin: a hooks module may import only its plugin's files and \"claude-code\"", args.Path)
				}
				real, err := filepath.EvalSymlinks(r.Path)
				if err != nil {
					return api.OnResolveResult{}, err
				}
				if !within(root, real) {
					return api.OnResolveResult{}, fmt.Errorf("import %q resolves to %s, outside the plugin root %s", args.Path, real, root)
				}
				se := api.SideEffectsTrue
				if !r.SideEffects {
					se = api.SideEffectsFalse
				}
				return api.OnResolveResult{Path: real, Namespace: "file", SideEffects: se}, nil
			})
		},
	}
}

// within reports whether path is root or inside it; both must be cleaned,
// symlink-free absolute paths.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func buildError(msgs []api.Message) error {
	var parts []string
	for _, m := range msgs {
		text := m.Text
		if m.Location != nil {
			text = fmt.Sprintf("%s:%d:%d: %s", m.Location.File, m.Location.Line, m.Location.Column, m.Text)
		}
		parts = append(parts, text)
	}
	return errors.New(strings.Join(parts, "; "))
}

type bundleCache struct {
	Version string            `json:"version"`
	Inputs  map[string]string `json:"inputs"` // path relative to the root -> sha256
	Code    string            `json:"code"`
}

func readBundleCache(path, root string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var c bundleCache
	if json.Unmarshal(data, &c) != nil || c.Version != bundleVersion || len(c.Inputs) == 0 {
		return "", false
	}
	for rel, want := range c.Inputs {
		got, err := fileHash(filepath.Join(root, rel))
		if err != nil || got != want {
			return "", false
		}
	}
	return c.Code, true
}

func writeBundleCache(path, root, metafile, code string) {
	var meta struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if json.Unmarshal([]byte(metafile), &meta) != nil || len(meta.Inputs) == 0 {
		return
	}
	c := bundleCache{Version: bundleVersion, Inputs: map[string]string{}, Code: code}
	rels := make([]string, 0, len(meta.Inputs))
	for rel := range meta.Inputs {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		h, err := fileHash(filepath.Join(root, rel))
		if err != nil {
			return
		}
		c.Inputs[rel] = h
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bundle-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
