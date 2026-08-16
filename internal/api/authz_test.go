package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
)

// TestEveryOperationHasExactlyOneAuthorizationRule is the guard on the whole
// scheme, and the direct counterpart of TestEveryOperationIsClassified.
//
// requirePermission refuses anything it cannot classify, so a forgotten route
// fails closed at runtime. That is the safe direction but a late one: the
// failure shows up as a 403 nobody can explain, in an environment where the
// logs may not be to hand. Here it shows up as a build failure naming the
// operation.
//
// It fails in both directions. An operation in two sets is worse than one in
// none: whichever set is checked first silently wins, and the other entry reads
// like a rule that is being applied when it is not.
func TestEveryOperationHasExactlyOneAuthorizationRule(t *testing.T) {
	s, _ := newTestServer(t)

	forEachOperation(s.api.OpenAPI(), func(op *huma.Operation) {
		id := op.OperationID

		var in []string

		if publicOperations[id] {
			in = append(in, "publicOperations")
		}

		if selfServiceOperations[id] {
			in = append(in, "selfServiceOperations")
		}

		if _, ok := operationAccess[id]; ok {
			in = append(in, "operationAccess")
		}

		switch len(in) {
		case 0:
			t.Errorf("%s (%s %s) is in none of publicOperations, selfServiceOperations "+
				"or operationAccess, so requirePermission refuses it", id, op.Method, op.Path)
		case 1:
		default:
			t.Errorf("%s appears in %s; an operation must be classified exactly once",
				id, strings.Join(in, " and "))
		}
	})
}

// TestAuthorizationSetsNameOperationsThatExist stops the maps rotting. An entry
// matching nothing is either a typo — leaving the route it meant to describe
// unclassified and therefore refused — or a leftover from a deleted operation.
func TestAuthorizationSetsNameOperationsThatExist(t *testing.T) {
	s, _ := newTestServer(t)

	registered := map[string]bool{}
	forEachOperation(s.api.OpenAPI(), func(op *huma.Operation) {
		registered[op.OperationID] = true
	})

	for id := range selfServiceOperations {
		if !registered[id] {
			t.Errorf("selfServiceOperations names %q, which is not a registered operation", id)
		}
	}

	for id := range operationAccess {
		if !registered[id] {
			t.Errorf("operationAccess names %q, which is not a registered operation", id)
		}
	}
}

// TestOrgScopedRulesLiveOnOrgScopedPaths ties the rule's scope to the route's
// shape.
//
// requirePermission reads the organization from {orgID}. A rule scoped to an
// organization on a path without that parameter would resolve to uuid.Nil and
// be refused as a scope mismatch — a 403 whose cause is nowhere in the request.
// The reverse is worse: an {orgID} in the path with a system-scoped rule means
// the organization in the URL is decorative and the caller's permissions were
// checked somewhere else entirely.
func TestOrgScopedRulesLiveOnOrgScopedPaths(t *testing.T) {
	s, _ := newTestServer(t)

	paths := map[string]string{}
	forEachOperation(s.api.OpenAPI(), func(op *huma.Operation) {
		paths[op.OperationID] = op.Path
	})

	param := "{" + orgIDParam + "}"

	for id, rule := range operationAccess {
		path, ok := paths[id]
		if !ok {
			continue // reported by TestAuthorizationSetsNameOperationsThatExist
		}

		scoped := strings.Contains(path, param)

		switch {
		case rule.Scope == authz.ScopeOrganization && !scoped:
			t.Errorf("%s needs %q in an organization but its path %q has no %s",
				id, rule.Permission, path, param)
		case rule.Scope == authz.ScopeSystem && scoped:
			t.Errorf("%s has %s in its path %q but its rule is system-scoped, so the "+
				"organization in the URL is never checked", id, param, path)
		}
	}
}

// TestAccessRulesNamePermissionsThatExist catches a rule left behind by a
// renamed permission. Authorize would refuse it as unknown, which is safe and
// completely opaque from the outside.
func TestAccessRulesNamePermissionsThatExist(t *testing.T) {
	for id, rule := range operationAccess {
		def, ok := authz.Lookup(rule.Permission)
		if !ok {
			t.Errorf("%s requires %q, which is not in the catalog", id, rule.Permission)

			continue
		}

		if def.Scope != rule.Scope {
			t.Errorf("%s declares scope %q but %q is scoped %q",
				id, rule.Scope, rule.Permission, def.Scope)
		}
	}
}

// TestGatedOperationsDeclareTheirRefusals keeps the contract honest. A status
// missing from Errors is missing from the OpenAPI document and from every
// generated client, even though the handler returns it — so a client written
// against the spec has no branch for being refused.
func TestGatedOperationsDeclareTheirRefusals(t *testing.T) {
	s, _ := newTestServer(t)

	forEachOperation(s.api.OpenAPI(), func(op *huma.Operation) {
		rule, ok := operationAccess[op.OperationID]
		if !ok {
			return
		}

		declared := map[int]bool{}
		for _, code := range op.Errors {
			declared[code] = true
		}

		if !declared[http.StatusForbidden] {
			t.Errorf("%s is behind %q but does not declare 403", op.OperationID, rule.Permission)
		}

		// Organization-scoped operations answer 404 to a caller who is not a
		// member, which is a different outcome from 403 and needs its own entry.
		if rule.Scope == authz.ScopeOrganization && !declared[http.StatusNotFound] {
			t.Errorf("%s is organization-scoped but does not declare 404, which is what "+
				"a non-member receives", op.OperationID)
		}
	})
}

// TestSelfServiceOperationsCannotBeGated pins the rule that a role configuration
// must never be able to lock someone out of their own account.
func TestSelfServiceOperationsCannotBeGated(t *testing.T) {
	for id := range selfServiceOperations {
		if _, ok := operationAccess[id]; ok {
			t.Errorf("%s is self-service but also carries a permission; a role change "+
				"could then take away access to the caller's own account", id)
		}
	}
}

// TestScopedRepositoryMethodsTakeAnOrganization is the encapsulation test for
// the resource half of a decision.
//
// The middleware can only answer "may you act in organization X". Whether the
// row being touched is *in* X is a different question, and the answer is that
// there is no way to ask for a row without naming an organization. This asserts
// the interface keeps that shape, so the check cannot be forgotten — it is not
// a check at all, it is the only available call.
func TestScopedRepositoryMethodsTakeAnOrganization(t *testing.T) {
	iface := reflect.TypeOf((*orgs.Repository)(nil)).Elem()
	want := reflect.TypeOf(uuid.UUID{})

	if iface.NumMethod() == 0 {
		t.Fatal("orgs.Repository has no methods; this test would pass vacuously")
	}

	for i := range iface.NumMethod() {
		method := iface.Method(i)
		sig := method.Type

		// Index 0 is context.Context, index 1 must be the organization.
		if sig.NumIn() < 2 {
			t.Errorf("orgs.Repository.%s takes no organization id", method.Name)

			continue
		}

		if sig.In(1) != want {
			t.Errorf("orgs.Repository.%s takes %s as its second parameter, want uuid.UUID "+
				"(the organization every scoped query must be filtered by)",
				method.Name, sig.In(1))
		}
	}
}

// TestHandlersDoNotReadTheOrgIDParameter is the other half of that
// encapsulation, and the one a type system cannot express.
//
// requirePermission resolves {orgID}, authorizes against it, and puts the
// result on the context as an authz.Grant. A handler that reached for the path
// parameter again could act on a different organization than the one that was
// checked — the parameter is declared only so huma documents it. Import paths
// and field names are read from the syntax tree rather than grepped, so a
// mention in a comment is not a false positive.
func TestHandlersDoNotReadTheOrgIDParameter(t *testing.T) {
	const dir = "v1"

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(dir, name)

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "OrgID" {
					return true
				}

				t.Errorf("%s: %s reads .OrgID from the request; the organization comes "+
					"from authz.GrantFrom(ctx), which is the one the middleware checked",
					filepath.ToSlash(path), fn.Name.Name)

				return true
			})

			return true
		})
	}
}

// TestTheProblemSchemaCarriesTheExtraFields is the contract half of the
// extended error document.
//
// huma reflects the schema for every error response off whatever
// huma.NewError returns, so the replacement in problem.Install is what puts
// code and required_permission into api/openapi.yaml. Get the ordering wrong —
// install after the routes are registered — and the responses would carry
// fields the contract never mentions, which every generated client would drop.
func TestTheProblemSchemaCarriesTheExtraFields(t *testing.T) {
	s, _ := newTestServer(t)

	spec, err := s.api.OpenAPI().YAML()
	if err != nil {
		t.Fatalf("YAML() = %v", err)
	}

	for _, field := range []string{"code", "required_permission"} {
		if !strings.Contains(string(spec), field+":") {
			t.Errorf("the OpenAPI document never mentions %q; the error schema was "+
				"reflected before problem.Install replaced huma.NewError", field)
		}
	}

	// And the content type has to stay what every existing client expects.
	if !strings.Contains(string(spec), "application/problem+json") {
		t.Error("error responses are no longer application/problem+json")
	}
}

// TestEveryPermissionGuardsAnOperation is the test whose absence let eight
// permissions sit in the catalog gating nothing.
//
// The catalog is served to administrators by GET /v1/permissions, so every key
// in it is offered as something to put in a role. A key no operation checks
// means somebody can build a role called "Auditor", hand it out, and watch it do
// nothing — with no error anywhere to explain why. That is the mirror image of
// TestOwnerCoversEveryOrganizationPermission, which guards the other direction:
// a permission no role grants.
//
// There is deliberately no exemption list. Adding one would make "define the
// permission now, wire it up later" a one-line decision, and later is exactly
// when it gets forgotten.
func TestEveryPermissionGuardsAnOperation(t *testing.T) {
	guarded := map[authz.Permission]bool{}
	for _, rule := range operationAccess {
		guarded[rule.Permission] = true
	}

	for _, def := range authz.Catalog() {
		if !guarded[def.Key] {
			t.Errorf("%q is in the catalog and offered to administrators in the role "+
				"editor, but no operation requires it — a role holding it would do nothing",
				def.Key)
		}
	}
}
