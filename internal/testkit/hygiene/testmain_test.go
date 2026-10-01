package hygiene

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDeferredCleanupBeforeExit fails when a TestMain defers cleanup and
// then calls os.Exit: os.Exit skips deferred calls, so the temp directory
// (often a freshly built binary) leaks on every run.
func TestNoDeferredCleanupBeforeExit(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil // not this test's job
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			var deferPos token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.DeferStmt:
					if deferPos == token.NoPos {
						deferPos = n.Pos()
					}
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Exit" {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" && deferPos != token.NoPos {
							t.Errorf("%s: TestMain defers at %s but calls os.Exit, which skips deferred calls",
								fset.Position(n.Pos()), fset.Position(deferPos))
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
