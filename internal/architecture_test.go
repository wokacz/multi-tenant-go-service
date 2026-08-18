package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	internalRoot = "."
	apiRoot      = "api"
	storeRoot    = "store"
)

// TestHumaStaysInsideTheAPIPackage keeps the web framework from spreading.
//
// A framework that leaks into the domain and the store is what turns "we could
// replace huma" into a rewrite: every layer ends up speaking in its request
// types and its error types. Reviews are not a reliable guard against that —
// the offending import always looks harmless on its own — so the rule is a test
// instead of a convention.
func TestHumaStaysInsideTheAPIPackage(t *testing.T) {
	forbidImport(t, internalRoot, apiRoot, "huma")
}

// TestGormStaysInsideTheStore is the same rule pointing the other way.
//
// Repositories translate driver errors into domain errors, which is what lets
// internal/api map user.ErrNotFound onto a 404 without knowing that a database
// was involved. An import of gorm above the store means some error is crossing
// that boundary untranslated.
func TestGormStaysInsideTheStore(t *testing.T) {
	forbidImport(t, internalRoot, storeRoot, "gorm.io")
}

// TestEntStaysInsideTheStore is the same rule for the ORM replacing GORM, and it is
// what keeps the migration (see ENT.md) from turning into a rewrite of the whole
// service.
//
// Generated ent types are a persistence detail. If one reaches the domain, the domain
// changes shape — and then the tests that were supposed to prove "behaves exactly as
// before" are themselves rewritten, which proves nothing. The repositories map
// ent entities onto internal/store/models, and that boundary is this test.
func TestEntStaysInsideTheStore(t *testing.T) {
	forbidImport(t, internalRoot, storeRoot, "entgo.io")
}

// forbidImport fails the test if any Go file under root imports a path
// containing substr. The subtree at exempt, when non-empty, is skipped.
//
// Import paths are read from the parsed syntax tree rather than matched with a
// grep over the source, so a mention in a comment or a string literal is not a
// false positive.
func forbidImport(t *testing.T, root, exempt, substr string) {
	t.Helper()

	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if exempt != "" && filepath.Clean(path) == filepath.Clean(exempt) {
				return fs.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}

			if strings.Contains(importPath, substr) {
				t.Errorf("%s imports %q: %q is not allowed outside %s",
					filepath.ToSlash(path), importPath, substr, allowedLocation(substr))
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

func allowedLocation(substr string) string {
	if substr == "huma" {
		return "internal/api"
	}

	return "internal/store"
}
