package problem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
)

// TestEveryCodeHasAMessage closes a failure mode that is invisible from the
// outside.
//
// newDocument sets Document.Code only when the catalog has error.<code>, so a code
// added without a translation produces a response that looks fine — right status,
// a detail carrying the raw key — and silently has no code field at all. Every
// client branches on code, so the refusal becomes unreadable to all of them while
// every test that only checks the status keeps passing. That is exactly what
// happened when invalid_invitation_batch was added.
//
// The constants are read from the syntax tree rather than listed here, so a code
// added next year is covered without anybody remembering this file.
func TestEveryCodeHasAMessage(t *testing.T) {
	const file = "document.go"

	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	catalog := i18n.Default()
	found := 0

	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}

		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, "Code") || i >= len(value.Values) {
					continue
				}

				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}

				code, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: %v", name.Name, err)

					continue
				}

				found++

				if !catalog.Has(i18n.Fallback, "error."+code) {
					t.Errorf("%s = %q has no error.%s in the fallback catalog, so "+
						"newDocument will answer without a code field at all",
						name.Name, code, code)
				}
			}
		}
	}

	// A parser that stopped matching would otherwise make this pass by finding
	// nothing to check.
	if found < 40 {
		t.Errorf("only %d code constants found in %s; the AST walk is not finding them",
			found, file)
	}
}
