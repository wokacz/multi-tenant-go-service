# .docker

Build inputs for the development environment. There is **no production
configuration here** — this directory exists to make `docker compose up`
pleasant, nothing more.

How to run either path (containers or host):
[docs/guides/001](../docs/guides/001_development_environment.md).
Why there is no production image, and why `loader/` is not in the image:
[docs/design/001](../docs/design/001_technology_stack.md#docker-compose).

| File | What it is |
| --- | --- |
| `compose.yml` | the development stack: Postgres, migrations, API, optional delve |
| `Dockerfile` | development image: Go toolchain, `air`, `dlv`, warmed module cache |
| `air.toml` | hot-reload configuration used by the `api` service |

## Why a `compose.yml` still sits in the repository root

Compose only auto-discovers `compose.yml` in the working directory. The file at
the root is a four-line `include` that points here and sets `project_directory`
to the repository root, so `docker compose up` does not need `-f` and relative
paths still mean the source tree rather than this directory.

## Why `.dockerignore` stays at the repository root

The build context is the repository root — the image copies `go.mod` / `go.sum`
from there; the rest of the source is bind-mounted at runtime. Docker looks for
`.dockerignore` at the **context** root, not next to the Dockerfile. Moving it
here would have no effect unless it were named `Dockerfile.dockerignore`, which
is the Dockerfile-specific escape hatch and not the file people look for.
