# Example API

A small Go service tracking users, their known devices, and login history.

> **Status: work in progress.** The domain models are implemented and tested.
> The storage layer (`internal/store`) and HTTP API are still stubs.

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
cp .env.example .env   # adjust if your Postgres differs
task run
```

## Tasks

Run `task` to list them, or:

| Task         | Description                                   |
| ------------ | --------------------------------------------- |
| `task build` | Build the API binary into `bin/`              |
| `task run`   | Run the API                                   |
| `task test`  | Run the test suite with the race detector     |
| `task cover` | Run tests and open an HTML coverage report    |
| `task lint`  | Run golangci-lint                             |
| `task fmt`   | Apply gofmt and goimports                     |
| `task tidy`  | Tidy and verify `go.mod`                      |
| `task check` | Everything CI should run (tidy + lint + test) |
| `task clean` | Remove build and coverage artefacts           |

## Layout

```
cmd/api/            entrypoint
internal/store/     persistence layer (stubs)
  models/           domain models and their invariants
loader/             seed data (empty)
migrations/         Atlas migrations (empty)
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