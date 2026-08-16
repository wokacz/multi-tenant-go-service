# Example API

A multi-tenant Go service: accounts, devices, organizations, and a role-based permission system that is enforced on the
server and tested as such.

Built on [chi](https://github.com/go-chi/chi) for routing and middleware,
[huma](https://huma.rocks/) for operations, validation and the OpenAPI document, GORM over pgx for storage,
and [Atlas](https://atlasgo.io/) for migrations.

## Documentation

The detailed documentation lives in [`docs/`](docs/README.md) **and is written in Polish**, since that is the team's
language. Start there.

|                                                                       |                                                                |
|-----------------------------------------------------------------------|----------------------------------------------------------------|
| [Architecture](docs/design/002_architecture.md)                       | layers, dependency direction, the boundaries enforced by tests |
| [API contract](docs/design/003_api_contract.md)                       | versioning, generated OpenAPI, DTOs                            |
| [Authorization](docs/design/007_authorization.md)                     | roles, permissions, organizations, audit                       |
| [Development environment](docs/guides/001_development_environment.md) | first run, commands, configuration                             |
| [Adding an endpoint](docs/guides/002_add_endpoint.md)                 | the checklist for the most common change                       |

The HTTP contract itself is [`api/openapi.yaml`](api/openapi.yaml) — generated from the handlers and committed, so a
change to it shows up as a reviewable diff rather than as an invisible side effect. In development it is also served at
`/docs` (Swagger UI), `/openapi.json` and `/openapi.yaml`; production serves none of them.

## Requirements

| Tool                                               | Purpose             |
|----------------------------------------------------|---------------------|
| [Go](https://go.dev/) 1.26+                        | toolchain           |
| [Task](https://taskfile.dev/)                      | task runner         |
| [Atlas](https://atlasgo.io/)                       | database migrations |
| [golangci-lint](https://golangci-lint.run/) **v2** | linting             |
| Docker                                             | Postgres            |

```bash
brew install go
brew install go-task/tap/go-task
brew install ariga/tap/atlas
brew install golangci-lint
```

`.golangci.yml` uses the v2 config schema. Installing with `go install` requires the `/v2/` module path — the old path
silently pulls v1, which cannot read the config:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

## Getting started

```bash
cp .env.example .env
docker compose up -d      # Postgres
task migrate              # schema
task run                  # http://127.0.0.1:4000
```

A fresh installation has nobody with permissions. Register an account, then grant it ownership:

```bash
task bootstrap -- -email you@example.com
```

Details, including why this is a deployment step rather than "first to register wins", are in
[the development environment guide](docs/guides/001_development_environment.md).

## Commands

`task check` is exactly what CI runs. `task --list` shows everything else.

```bash
task check      # tidy + lint + test + openapi:check
task test       # go test ./... -race
task run
task migrate
```

Tests need no database by default; the repository tests skip unless
`POSTGRES_TEST=1` is set. CI sets it against a Postgres service container, so the SQL they cover is checked on every
push.

## License

MIT.
