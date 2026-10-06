package crash

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// guardExempt lists directories whose goroutines need no guard: test
// infrastructure that never runs inside kiln, and this package, whose Go
// is the guard.
var guardExempt = []string{
	"internal/testkit",
	"internal/crash",
}

// Every goroutine kiln starts must be guarded, or a panic in it ends the
// process with the terminal still in raw mode. A `go` statement is
// guarded when it is a func literal whose first statement is `defer
// crash.Guard()`; everything else should use crash.Go.
func TestEveryGoroutineIsGuarded(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var unguarded []string
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd/kiln"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				for _, ex := range guardExempt {
					if rel == ex {
						return filepath.SkipDir
					}
				}
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				g, ok := n.(*ast.GoStmt)
				if !ok || deferGuardFirst(g) {
					return true
				}
				p := fset.Position(g.Pos())
				unguarded = append(unguarded, rel+":"+strconv.Itoa(p.Line))
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(unguarded) > 0 {
		t.Errorf("unguarded go statements (use crash.Go, or defer crash.Guard() first):\n  %s", strings.Join(unguarded, "\n  "))
	}
}

func deferGuardFirst(g *ast.GoStmt) bool {
	lit, ok := g.Call.Fun.(*ast.FuncLit)
	if !ok || len(lit.Body.List) == 0 {
		return false
	}
	d, ok := lit.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	sel, ok := d.Call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Guard" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "crash"
}
