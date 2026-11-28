package wireless

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every string the status page can show must exist in the translation table, or
// the page comes out half English and half French - which is exactly what
// happened: the first screen (hero, chips, Bluetooth debug) was added after the
// table and 32 of its strings had no entry.
//
// The test reads the package source and checks each literal that reaches a
// translation point. It is the guard that keeps a new label from shipping
// untranslated.
func TestEveryVisibleStringIsTranslated(t *testing.T) {
	fr := translations["fr"]
	if fr == nil {
		t.Fatal("no French table")
	}

	// Literals that are protocol headers, not user-facing text.
	notText := map[string]bool{"Cache-Control": true, "Content-Type": true}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join("..", "..", "cmd", "cargadgetd", "main.go"))

	fset := token.NewFileSet()
	var missing []string
	seen := map[string]bool{}

	check := func(pos token.Position, lit *ast.BasicLit) {
		if lit == nil || lit.Kind != token.STRING {
			return
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil || key == "" || notText[key] || seen[key] {
			return
		}
		seen[key] = true
		if _, ok := fr[key]; !ok {
			missing = append(missing, pos.String()+": "+key)
		}
	}

	for _, name := range files {
		base := filepath.Base(name)
		if strings.HasSuffix(base, "_test.go") || base == "i18n.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				// T(lang, key) and TF(lang, tmpl, args...)
				if (fn.Name == "T" || fn.Name == "TF") && len(call.Args) >= 2 {
					if lit, ok := call.Args[1].(*ast.BasicLit); ok {
						check(fset.Position(lit.Pos()), lit)
					}
				}
			case *ast.SelectorExpr:
				switch fn.Sel.Name {
				case "Set", "Setf", "SetStatic", "Step", "Phase", "Error":
					if len(call.Args) == 0 {
						break
					}
					if lit, ok := call.Args[0].(*ast.BasicLit); ok {
						check(fset.Position(lit.Pos()), lit)
					}
				}
			}
			return true
		})
	}

	if len(missing) > 0 {
		t.Fatalf("%d string(s) reachable on the page have no French entry:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}
