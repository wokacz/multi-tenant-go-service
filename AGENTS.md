# AGENTS.md

Working manual for AI agents in this repository. Read this first, then the document it points you at for the task at
hand.

**Language:** code, identifiers and comments are **English**. The detailed documentation in [`docs/`](docs/README.md) is
**Polish** — that is deliberate, not drift. Write code in English; if you write documentation, write it in Polish.

## Source hierarchy

```
Source code            ← the truth about what happens
    ↓
docs/                  ← the truth about why
    ↓
AGENTS.md              ← rules and a map
    ↓
General conventions    ← only when nothing above answers the question
```

When documentation contradicts the code, **the code wins** — and say so in your response, then fix the document in the
same change. Documentation that has been allowed to lie is worse than none, because it is trusted.

When you cannot establish something from the repository, **say so**. Do not guess and do not invent a convention.

## Non-negotiable

These are enforced by tests, not by review, because every violation looks harmless in isolation.

1. **huma only inside `internal/api`. entgo.io only inside `internal/store`.**
   Enforced by `internal/architecture_test.go` (note: it lives at the root of
   `internal/`, not under `internal/api/`).
2. **The consumer owns the repository interface.** It is declared in
   `internal/domain/<thing>/repository.go` and implemented in
   `internal/store/repositories/<thing>.go`. The store depends on the domain, never the reverse.
3. **Models never reach JSON.** DTOs live in `internal/api/v1` and never leave it.
4. **Every operation is classified in exactly one** of `publicOperations`,
   `selfServiceOperations`, `operationAccess`.
5. **Handlers read the organization from `authz.GrantFrom(ctx)`**, never from
   `in.OrgID`.
6. **Storage errors are translated at the storage boundary.** `problem` maps domain vocabulary only; a driver error
   reaching it becomes an opaque 500.

If a task seems to require breaking one of these, stop and explain the conflict instead of working around the test.

## Repository map

```
cmd/api/                entrypoint — wiring only, no logic
cmd/bootstrap/          grants the first owner (deployment step)
cmd/openapi/            writes api/openapi.yaml
internal/
  api/                  the only place huma appears
    authz.go            operationAccess, selfServiceOperations, requirePermission
    middleware.go       publicOperations, requireBearer, rate limiting
    problem/            domain error → RFC 7807, error codes, i18n of messages
    v1/                 operations; one file per module
  auth/                 JWT signing and parsing
  config/               process configuration
  i18n/                 language negotiation + embedded locale catalogs
  domain/               business rules; one directory per entity
    audit/ authz/ orgs/ user/
  store/                the only place entgo.io appears
    ent/schema/         the source of truth for the schema
    models/             domain structs; repositories map ent entities onto these
    repositories/       implementations
      memory/           in-memory fake, shared by every test package
tools/entgen/           SEPARATE Go module — the ent code generator
migrations/             Atlas migrations, generated
compose.yml             include pointer so `docker compose up` works from the root
.docker/                compose.yml, Dockerfile, air.toml — not a production image
api/openapi.yaml        the HTTP contract, generated and committed
docs/                   design decisions and how-to guides (Polish)
```

## Commands

```bash
task up                 # full stack in containers; see docs/guides/001
task check              # tidy + lint + test + openapi:check — exactly what CI runs
task test               # go test ./... -race
task lint
task run                # API on the host (Postgres still from compose)
task migrate
task migrate:diff NAME=<name>    # after changing a model
task openapi                     # after changing a handler
```

Database-backed tests skip unless enabled:

```bash
POSTGRES_TEST=1 go test ./internal/store/repositories -v
```

## Before you finish

Run `task check`. If you touched any of these, run the extra step too:

| Touched                             | Also required                                                  |
|-------------------------------------|----------------------------------------------------------------|
| a handler, DTO, or `huma.Operation` | `task openapi` and commit `api/openapi.yaml`                   |
| a schema in `internal/store/ent/schema` | `task ent:generate`, then `task migrate:diff NAME=…`       |
| a repository interface              | update the fake in `internal/store/repositories/memory`        |
| a permission                        | translations in **every** `internal/i18n/locales/*.json`       |
| `go.mod`                            | `task tidy` — a bare `go mod tidy` misses the `tools/entgen` module |

`task check` fails on an uncommitted `api/openapi.yaml` regeneration and on a model changed without its migration. Both
failures name the fix.

## Guard tests

These fail the build on an incomplete change. Read the failure message — each one states what is missing and why it
matters.

| Test                                               | File                                  | Demands                                                      |
|----------------------------------------------------|---------------------------------------|--------------------------------------------------------------|
| `TestHumaStaysInsideTheAPIPackage`                 | `internal/architecture_test.go`       | no huma import outside `internal/api`                        |
| `TestGormDoesNotAppear`                            | `internal/architecture_test.go`       | the previous ORM is not imported anywhere under `internal/`  |
| `TestEntStaysInsideTheStore`                       | `internal/architecture_test.go`       | no entgo.io import outside `internal/store`                  |
| `TestEveryOperationIsClassified`                   | `internal/api/middleware_test.go`     | `publicOperations` and the spec's `Security` agree both ways |
| `TestEveryOperationHasExactlyOneAuthorizationRule` | `internal/api/authz_test.go`          | every operation in exactly one of the three sets             |
| `TestOrgScopedRulesLiveOnOrgScopedPaths`           | `internal/api/authz_test.go`          | `ScopeOrganization` ⟺ `{orgID}` in the path                  |
| `TestGatedOperationsDeclareTheirRefusals`          | `internal/api/authz_test.go`          | 403 (and 404 when org-scoped) in `Errors`                    |
| `TestSelfServiceOperationsCannotBeGated`           | `internal/api/authz_test.go`          | `/v1/me/*` never behind a permission                         |
| `TestHandlersDoNotReadTheOrgIDParameter`           | `internal/api/authz_test.go`          | no handler reads `in.OrgID`                                  |
| `TestScopedRepositoryMethodsTakeAnOrganization`    | `internal/api/authz_test.go`          | `orgID` is the second parameter of every scoped method       |
| `TestEveryPermissionGuardsAnOperation`             | `internal/api/authz_test.go`          | no permission in the catalog without an operation            |
| `TestOwnerCoversEveryOrganizationPermission`       | `internal/domain/authz/authz_test.go` | no permission that no role grants                            |
| `TestTheSnapshotAgreesWithEnforcement`             | `internal/api/snapshot_http_test.go`  | a probe for every org-scoped operation                       |
| `TestSystemScopeIsEnforcedEndToEnd`                | `internal/api/platform_http_test.go`  | a probe for every system-scoped operation                    |
| `TestEveryMutatingOperationIsAudited`              | `internal/api/audit_http_test.go`     | every gated operation classified as mutating or read-only    |
| `TestEveryLocaleDefinesTheSameKeys`                | `internal/i18n/i18n_test.go`          | identical key sets across locales                            |
| `TestRateLimitAppliesToEveryCostlyRoute`           | `internal/api/middleware_test.go`     | costly routes are rate limited                               |

Adding an operation typically trips 4–6 of these. That is the design working:
the checklist in [guides/002](docs/guides/002_add_endpoint.md) exists so you satisfy them in one pass instead of
iterating on failures.

## Conventions

- **Tests:** standard library `testing` only. No testify, no mocking library. Assertions are plain `if`; messages read
  `Name() = %v, want %v`.
- **Test names state the property**, not the method:
  `TestTheLastOwnerCannotBeDemoted`, not `TestSetMemberRoles`.
- **Comments explain why, not what**, and are load-bearing — several record traps that already cost time. Match the
  surrounding density; do not strip them.
- **Errors:** sentinel `var ErrX = errors.New("<package>: ...")` in the domain; repositories wrap with
  `fmt.Errorf("store: <op>: %w", err)`.
- **Collections** are built with `make([]T, 0, n)` so empty serialises as `[]`.
- **Compile-time assertions** tie struct tags to domain constants and implementations to interfaces
  (`var _ user.Repository = (*User)(nil)`). Keep them.

## Do not hand-edit

| File                      | Instead                                          |
|---------------------------|--------------------------------------------------|
| `api/openapi.yaml`        | `task openapi`                                   |
| `migrations/*.sql`        | `task migrate:diff NAME=…`                       |
| `migrations/atlas.sum`    | regenerated by Atlas; a hand edit invalidates it |
| `go.sum`, `tools/entgen/go.sum` | `task tidy`                          |

Do not add a dependency without saying why in your response. The direct dependency list is short on purpose — see
[design/001](docs/design/001_technology_stack.md) for what is deliberately absent.

## Traps

| Trap                                                   | Consequence                                                                                         |
|--------------------------------------------------------|-----------------------------------------------------------------------------------------------------|
| `go mod tidy` without `-C tools/entgen`                | second module drifts; CI fails                                                                      |
| golangci-lint installed without the `/v2/` module path | silently installs v1, which cannot read `.golangci.yml`                                             |
| schema changed without `task ent:generate`             | `ent:check` fails; the committed client is stale                                                    |
| `AddX(1)` plus a second statement for the cap          | concurrent attempts lose increments; keep the counter and the cap in one `UPDATE`                   |
| delete by id without loading the row first             | `IsProtected` / `IsSystem` are never seen; load, then `RefuseIfProtected` / `RefuseDelete`          |
| read-modify-write on a counter                         | concurrent requests lose increments; use one conditional `UPDATE`                                   |
| query that bypasses the ent client                     | the soft-delete interceptor does not apply; filter `deleted_at` yourself                            |
| unique index on a nullable column                      | in Postgres two `NULL`s do not collide                                                              |
| new route with a path variable                         | the rate limiter matches literal paths; see `isMembersPath`                                         |

## Where to look

Start at [`docs/README.md`](docs/README.md). Direct routes:

| Task                                                    | Document                                               |
|---------------------------------------------------------|--------------------------------------------------------|
| add an HTTP operation                                   | [guides/002](docs/guides/002_add_endpoint.md)          |
| add or change a model                                   | [guides/003](docs/guides/003_models_and_migrations.md) |
| add a repository                                        | [guides/004](docs/guides/004_repositories.md)          |
| write a query, transaction, or migration-sensitive code | [guides/005](docs/guides/005_database_access.md)       |
| add a permission                                        | [guides/006](docs/guides/006_add_permission.md)        |
| write tests                                             | [guides/007](docs/guides/007_write_tests.md)           |
| add a message or language                               | [guides/008](docs/guides/008_add_translation.md)       |
| understand a boundary before changing it                | [design/002](docs/design/002_architecture.md)          |
| understand an authorization decision                    | [design/007](docs/design/007_authorization.md)         |
| understand an error status or code                      | [design/008](docs/design/008_errors_and_i18n.md)       |

## Known gaps

Documented as absent in [design/007](docs/design/007_authorization.md). Do not
"fix" them by inventing a design; ask first.

- `role_translations` exists as a table but nothing reads or writes it — custom role names are single-language.
- No endpoint changes `User.Locale` after registration.
- No permission cache; permissions are resolved per request, deliberately.

## How to work

1. **Read before writing.** Find the existing pattern — this repository has one for almost everything, and the guides
   name it.
2. **Do not invent a second pattern.** If two shapes could work, use the one already in the tree.
3. **Check the decision before changing the architecture.** The boundaries above are load-bearing and documented with
   their reasoning.
4. **Prefer the minimal change** that fits the existing structure.
5. **Do not delete code, comments or documentation** without saying why. The comments in particular record traps.
6. **Run `task check`** and any extra step from the table above.
7. **Report faithfully.** If tests fail, show the output. If you skipped part of the task, say which part and why. If
   something could not be verified, mark it as unverified rather than asserting it.
