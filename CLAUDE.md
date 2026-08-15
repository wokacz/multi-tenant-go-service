# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Everything goes through [Task](https://taskfile.dev/). `task check` is what CI runs.

```bash
task check                        # tidy + lint + test + openapi:check
task run                          # run the API (needs Postgres up and migrated)
task test                         # go test ./... -race
task lint                         # golangci-lint run ./...
task openapi                      # regenerate api/openapi.yaml after changing a handler
task migrate                      # apply migrations
task migrate:diff NAME=<name>     # generate a migration after changing a GORM model
```

Local setup: `cp .env.example .env`, `docker compose up -d`, `task migrate`, `task run`.

Single test / package:

```bash
go test ./internal/store/models -run TestDeviceTrustLifecycle -v
go test ./internal -run TestHumaStaysInsideTheAPIPackage -v
```

Tests need no database — the model tests use `schema.Parse` in memory, and the
architecture tests parse source. Anything requiring Postgres is run by hand
against the compose stack.

### Toolchain gotchas

- **golangci-lint must be v2.** `.golangci.yml` uses the v2 config schema; the
  non-`/v2/` module path silently installs v1, which cannot read it and fails
  with a confusing error.
- **There are two Go modules.** The root, and `loader/`. `task tidy` handles
  both; a bare `go mod tidy` misses the loader.
- **The application never reads `.env` itself.** It reads the environment;
  Taskfile's `dotenv:` is what loads the file.

## Architecture

A layered HTTP service: chi for routing/middleware, [huma](https://huma.rocks/)
for operations, validation and OpenAPI generation, GORM over pgx for storage,
Atlas for migrations.

### Dependencies point inwards, and it is enforced

`internal/api/architecture_test.go` fails the build if:

- anything under `internal/` outside `internal/api` imports **huma**
- anything under `internal/` outside `internal/store` imports **gorm**

These are not style rules. Before touching imports in those trees, understand
why the boundary exists rather than working around the test. The test lives in
`internal/architecture_test.go`.

### The error chain is the spine of the design

An error changes vocabulary exactly twice:

1. **Repository** (`internal/store/repositories/user.go`) turns driver errors into domain
   errors: `gorm.ErrDuplicatedKey` → `user.ErrEmailTaken`. GORM's error types
   stop here.
2. **`internal/api/problem`** turns domain errors into HTTP statuses:
   `user.ErrNotFound` → 404. It knows nothing about databases.

Anything unmapped becomes an opaque 500 with the real error logged against the
request id. That is deliberate — raw errors carry table names and query
fragments. To add a mapping, add a domain error and a case in `problem.Error`.

`problem` is a separate package rather than `internal/api/errors.go` because
`internal/api` imports `internal/api/v1` to register routes, so `v1` cannot
import its parent. A file in package `api` would be an import cycle; a package
named `errors` would shadow the standard library. The name is the RFC 7807
document this layer actually emits (`application/problem+json`).

### Who owns which interface

`internal/domain/user/repository.go` declares `Repository`;
`internal/store/repositories/user.go` implements it. **The consumer owns the
interface**, so the store depends on the domain and never the reverse. New
modules follow the same shape: `internal/domain/<thing>/repository.go` for the
interface, `internal/store/repositories/<thing>.go` for the GORM implementation,
with a `var _ <thing>.Repository = (*T)(nil)` assertion.

`internal/` splits plumbing from business: `api`, `auth`, `config` and `store`
are infrastructure; every entity lives under `internal/domain/`. That keeps a
domain `config` from colliding with process configuration, and keeps `auth`
(token crypto) out of the domain tree.

### The API contract

- **Versioned from the first route.** `/v1` lives in the path *and* the
  directory tree (`internal/api/v1`). A v2 is a new package beside it.
Operational endpoints (`/health`) sit outside the version deliberately —
probes should not break on a contract bump. The OpenAPI document is committed
and is not served in production.
- **DTOs never leave `internal/api`.** `v1/dto.go` holds the wire types and the
  single model→DTO function. `models.User` carries `PasswordHash`,
  `IsProtected` and `DeletedAt`; it must not be marshalled.
- **`api/openapi.yaml` is generated and committed.** `api.Spec()` renders it
  from a *fixed* config, not the running one, so the file does not change with
  the developer's port. `task openapi:check` (and CI) fail when it is stale.

When adding an operation, `Errors: []int{...}` on the `huma.Operation` must list
every status the handler can return. A status missing there is missing from the
spec and from generated clients even though the handler returns it.

### Authentication is default-deny

`requireBearer` in `internal/api/middleware.go` authenticates every operation
whose id is *not* in `publicOperations`. Do not switch it back to reading the
operation's `Security` block: that fails open, and a route registered without
`Security` would be silently anonymous.

A new operation therefore needs two things that must agree:

- `Security: []map[string][]string{{"bearer": {}}}` if it is protected, so the
  spec and generated clients know.
- an entry in `publicOperations` if it is not.

`TestEveryOperationIsClassified` fails when they disagree in either direction.

### Devices, and why the token carries one

The JWT holds a device id (`did`) as well as the subject and the session epoch.
The bearer middleware checks on every request that the device still exists and
is not revoked, which is what makes `DELETE /v1/me/devices/{id}` take effect
now rather than at token expiry. Removing that check turns device revocation
back into a promise the API does not keep.

Clients are recognised by an opaque `X-Device-Token`; only its SHA-256 is
stored, in `Device.Fingerprint` (hence `size:64`).

### Attempt counters move in SQL, never in Go

`FailPasswordReset` and `FailTwoFactorChallenge` increment and, at the cap,
spend the code in one conditional `UPDATE`. Do not "simplify" either into a
load, `Attempts++`, save: overlapping guesses then read the same value and
write the same value, and a late writer can restore a `consumed_at` another
request had just set. `TestFailPasswordResetUnderConcurrency` covers this and
needs Postgres.

### Test doubles

`internal/store/repositories/memory` is a real `user.Repository` in maps, shared
by the API and domain suites. Add new interface methods there too — two stubs
of a twenty-method interface drift, and the suite with the laxer one stops
testing anything.

Tests build the service with `user.WithBcryptCost(bcrypt.MinCost)`. Without it
the API suite spends ~40s under `-race` deriving keys nothing checks.

`internal/store/repositories/user_postgres_test.go` covers the SQL the in-memory
fake reimplements in Go — the conditional `UPDATE`s, the explicit `::inet` cast,
`SELECT ... FOR UPDATE`, `NULLS LAST`. It skips unless `POSTGRES_TEST=1`:

```bash
docker compose up -d && task migrate
POSTGRES_TEST=1 go test ./internal/store/repositories -v
```

### Schema

The **GORM models are the source of truth**. `loader/` prints the DDL they
imply, Atlas diffs it against `migrations/` and writes a versioned migration.
`AutoMigrate` is never called — it guesses at column changes and never drops
anything, so it would drift silently. CI fails if a model changed without its
migration.

`loader/` is a separate module because `atlas-provider-gorm` pulls in every GORM
driver plus gRPC and cloud SDKs, none of which belong in the API's dependency
graph. It can still import the parent's `internal/` packages: the `internal`
rule follows the directory tree, not the module boundary.

### Configuration

`config.Load()` returns **all** validation errors at once via `errors.Join`,
because configuration is fixed by editing a file and restarting. Timeouts, pool
sizes and `Env` live there; `Env` gates the log format, whether `/docs` and
`/openapi.json` are served (production serves neither), TLS and secret
requirements.

`cmd/api/main.go` only assembles dependencies — no logic, not even the logger
construction (that is `config.NewLogger`). It does set `time.Local = time.UTC`,
because pgx decodes `timestamptz` into the local zone and timestamps would
otherwise serialise differently per machine.

## Conventions in existing code

Comments explain *why*, not what, and are worth preserving — several record
non-obvious traps (chi's `RealIP` being spoofable, Postgres 18's changed volume
mount, bcrypt's 72-byte limit, composite indexes degrading silently). Match that
density rather than stripping them.
