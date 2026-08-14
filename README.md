# Example API

A small Go service tracking users, their known devices, and login history.

> **Status: work in progress.** Users are implemented end to end — model,
> repository, service and `/v1/users` routes — and serve as the template for
> the modules that follow. Devices and login events have models but no
> repository or routes yet.

The API is built on [chi](https://github.com/go-chi/chi) for routing and
middleware and [huma](https://huma.rocks/) for operations, validation and the
OpenAPI document.

## Conventions

These are the rules the layout exists to serve. The first two are enforced by
tests rather than by review, because an import that breaks them always looks
harmless on its own.

**1. huma stays inside `internal/api`, gorm stays inside `internal/store`.**
A framework that reaches the domain turns "we could replace huma" into a
rewrite. `internal/api/architecture_test.go` parses every import under
`internal/` and fails on a violation, naming the file.

**2. A repository interface belongs to the package that uses it.**
[`internal/user/repository.go`](internal/user/repository.go) declares what the
user module needs; [`internal/store/user.go`](internal/store/user.go) implements
it. The dependency points inwards — the store knows about the domain, never the
reverse — and the interface lists only what is actually used.

**3. DTOs never leave `internal/api`; models never reach JSON.**
[`v1/dto.go`](internal/api/v1/dto.go) holds the wire types and the single
function that converts a model into one. `models.User` carries `PasswordHash`,
`IsProtected` and `DeletedAt`; encoding it directly would leak the first and
expose internal lifecycle in the rest. Going through a DTO also means adding a
column cannot silently widen the API.

**4. Storage errors are translated at the storage boundary.**
Repositories turn `gorm.ErrRecordNotFound` into `user.ErrNotFound`, so
[`apierr`](internal/api/apierr/errors.go) maps domain vocabulary onto status
codes without knowing a database exists. Anything unmapped becomes an opaque
500 with the real error in the log — unmapped errors carry table names and
query fragments.

**5. The API is versioned from the first route.**
`/v1` appears in the path and in the directory tree
([`internal/api/v1`](internal/api/v1)), so a v2 is a package beside it rather
than a rewrite. Operational endpoints — `/health`, the OpenAPI document — sit
outside the version: they are not part of the contract, and a probe should not
need reconfiguring when the contract version changes.

**6. `main.go` is boring.** It assembles dependencies and nothing else. Even
the log format lives in `internal/config`, so a second entrypoint composes the
same pieces without inheriting decisions made in `main`.

**7. Dependencies move on purpose.** Versions are pinned in `go.mod` and
updated through Dependabot pull requests. huma is excluded from automatic minor
bumps: it generates the OpenAPI document, so a minor release can move the
committed contract.

## API documentation

The OpenAPI document is generated from the handlers, not maintained by hand:
huma derives each operation's schema from its Go input and output types, so the
spec cannot drift from the code.

It is also **committed to the repository** at
[`api/openapi.yaml`](api/openapi.yaml). Generated-and-committed is the point:
the contract stays code-first, and a change to it still shows up as a reviewable
diff in the pull request rather than as an invisible side effect of editing a
handler. `task openapi` regenerates it and `task openapi:check` fails when it is
stale, so CI catches a forgotten regeneration.

The document is rendered from a fixed configuration rather than the running one,
so it does not change depending on which port the developer happened to use.

With the server running:

| Path            | What it is                                    |
| --------------- | --------------------------------------------- |
| `/docs`         | Swagger UI                                    |
| `/openapi.json` | OpenAPI 3.1 document                          |
| `/openapi.yaml` | the same document as YAML                     |
| `/schemas/...`  | individual JSON Schemas, for editor autocomplete |

Swagger UI is huma's built-in renderer, selected with
`cfg.DocsRenderer = huma.DocsRendererSwaggerUI` in
[`server.go`](internal/api/server.go). It is worth using as shipped rather than
serving a hand-written page: huma pins the asset versions, attaches subresource
integrity hashes and sends a matching `Content-Security-Policy`, all of which a
hand-rolled page tends to drop.

**In production (`ENV=production`) `/docs` returns 404** while
`/openapi.json` keeps working, so generated clients still build without
publishing an explorable map of the API.

Two things are worth keeping up as operations are added, because both flow
straight into the docs and into generated clients:

- `Summary`, `Description` and `Tags` on the `huma.Operation`.
- `Errors: []int{...}`, listing every status the handler can return. A status
  that is missing there is missing from the spec, even though the handler
  returns it.

### Endpoints

| Method | Path              | Description                                           |
| ------ | ----------------- | ----------------------------------------------------- |
| `GET`  | `/health`         | 200 when the service can serve traffic, 503 when not. |
| `POST` | `/v1/users`       | Register a user. 201 with `Location`, 409 on a duplicate email. |
| `GET`  | `/v1/users/{id}`  | Fetch a user. 404 when there is none.                 |

`/health` reaches the database rather than only reporting that the process is
up — an instance that cannot query Postgres cannot serve anything, and should
leave the load balancer's rotation. The check runs under its own short deadline
(`HealthTimeout`), since whatever polls it is usually on a tighter clock than an
ordinary caller.

## Requirements

| Tool                                        | Purpose             |
| ------------------------------------------- | ------------------- |
| [Go](https://go.dev/) 1.26+                 | toolchain           |
| [Task](https://taskfile.dev/)               | task runner         |
| [Atlas](https://atlasgo.io/)                | database migrations |
| [golangci-lint](https://golangci-lint.run/) | linting             |

```bash
brew install go
brew install go-task/tap/go-task
brew install ariga/tap/atlas
brew install golangci-lint
```

`.golangci.yml` uses the v2 config schema, so if you install the linter with
`go install` rather than Homebrew, make sure to take the `/v2/` module path —
the old path silently pulls v1, which cannot read the config:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

## Getting started

```bash
cp .env.example .env      # adjust if your Postgres differs
docker compose up -d      # Postgres on POSTGRES_PORT, bound to loopback
task migrate              # create the schema
task run
```

The API refuses to start without a reachable database, so bring the compose
stack up first. If port 5432 is already taken by another project, change
`POSTGRES_PORT` in `.env` — both the app and compose read it.

## Tasks

Run `task` to list them, or:

| Task                              | Description                                        |
| --------------------------------- | -------------------------------------------------- |
| `task build`                      | Build the API binary into `bin/`                   |
| `task run`                        | Run the API                                        |
| `task test`                       | Run the test suite with the race detector          |
| `task cover`                      | Run tests and open an HTML coverage report         |
| `task lint`                       | Run golangci-lint                                  |
| `task fmt`                        | Apply gofmt and goimports                          |
| `task tidy`                       | Tidy and verify both modules                       |
| `task openapi`                    | Regenerate `api/openapi.yaml`                      |
| `task openapi:check`              | Fail if the committed spec is stale                |
| `task migrate`                    | Apply pending migrations                           |
| `task migrate:diff NAME=<name>`   | Generate a migration from model changes            |
| `task migrate:status`             | Show which migrations have been applied            |
| `task check`                      | Everything CI runs                                 |
| `task clean`                      | Remove build and coverage artefacts                |

## Migrations

The GORM models are the source of truth. `loader/` prints the DDL they imply,
Atlas diffs that against `migrations/` and writes a versioned migration:

```bash
task migrate:diff NAME=add_device_label   # after changing a model
task migrate                              # apply
```

`AutoMigrate` is never called. It guesses at column changes and never drops
anything, so it would drift from the migrations with nothing reporting it. CI
runs the diff and fails if it produces a file — that is a model changed without
its migration.

`loader/` is a **module of its own**: `atlas-provider-gorm` depends on every
GORM driver plus a slice of gRPC and cloud SDKs, and none of that belongs in the
dependency graph of a service that talks only to Postgres. Nested modules can
still import the parent's `internal/` packages, because the `internal` rule
follows the directory tree rather than the module boundary.

## Layout

```
api/openapi.yaml       the contract, generated from code and committed
cmd/
  api/                 entrypoint — wiring only
  openapi/             writes api/openapi.yaml
internal/
  config/              environment-backed configuration, logger construction
  api/                 the only place huma appears
    server.go          router, middleware, OpenAPI config, health, lifecycle
    architecture_test.go  enforces the import rules
    apierr/            domain error → HTTP status, in one place
    v1/                version 1 of the contract
      router.go        what /v1 consists of
      dto.go           wire types and the model → DTO conversion
      users.go         user operations
  user/                user domain: Repository interface + Service
  store/               persistence
    postgres.go        connection pool, GORM setup
    user.go            implements user.Repository
    models/            domain models and their invariants
loader/                separate module: prints model DDL for Atlas
migrations/            Atlas migrations
```

## Data model

Three entities, all keyed by a time-ordered UUIDv7 so inserts stay clustered in
the primary key index:

- **User** — soft-deletable, and optionally flagged `IsProtected` to block
  deletion entirely. Deleting a user revokes their devices, since a soft delete
  never fires the foreign-key cascade.
- **Device** — identified by a fingerprint that is unique _per user_. Carries an
  explicit trust lifecycle: `Trust` / `Revoke` / `Unrevoke`. Lifting a
  revocation deliberately does not restore trust — the user has to confirm the
  device again.
- **LoginEvent** — an audit trail of login attempts, indexed on
  `(user_id, created_at)` and `(device_id, created_at)`.

Composite indexes are asserted in
[`schema_test.go`](internal/store/models/schema_test.go): GORM silently degrades
a composite index to a single-column one when the tags are wrong, and nothing
else catches it.

## License

[MIT](LICENSE)