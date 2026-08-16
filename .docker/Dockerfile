# Development image for the API. There is deliberately no production stage
# here: this file exists to make `docker compose up` pleasant, and a build
# target nobody deploys is a build target nobody keeps working.
#
# The source is bind-mounted at runtime rather than copied, so the image holds
# only the toolchain and the warmed module cache.

FROM golang:1.26-alpine

# git: the Go toolchain shells out to it for some module operations.
# curl: the container's own healthcheck.
RUN apk add --no-cache git curl

# UID/GID are build arguments so the container user can be lined up with the
# host user on Linux, where a bind mount keeps the host's ownership. Docker
# Desktop on macOS maps ownership for us, so the defaults are fine there.
ARG UID=1000
ARG GID=1000

RUN addgroup -g ${GID} app \
 && adduser -u ${UID} -G app -D -h /home/app app

# Caches live outside the bind-mounted source so nothing in the repository is
# written to from inside the container, and they are backed by named volumes so
# a rebuild does not start from nothing.
ENV GOCACHE=/home/app/.cache/go-build \
    GOMODCACHE=/go/pkg/mod \
    CGO_ENABLED=0

RUN mkdir -p ${GOCACHE} /tmp/air \
 && chown -R app:app /home/app /tmp/air /go

WORKDIR /app

# Only the manifests, so this layer is rebuilt when dependencies change rather
# than on every source edit. The modules land in the image and are copied into
# the named volume the first time it is used.
#
# loader/ is deliberately absent: it pulls every GORM driver plus cloud SDKs for
# the sake of `task migrate:diff`, which runs on the host. See
# docs/design/001_technology_stack.md.
COPY --chown=app:app go.mod go.sum ./

USER app

# air rebuilds and restarts on a file change; delve is the debugger the
# api-debug service runs. Both are pinned — an unpinned tool is a rebuild that
# behaves differently on a colleague's machine.
#
# Installed as `app` so they land in the app-owned module cache rather than
# being copied across a root-to-app chown.
#
# delve tracks the Go release closely and refuses to run against a toolchain
# newer than it knows. Bumping Go here means bumping delve in the same change.
RUN go install github.com/air-verse/air@v1.67.4 \
 && go install github.com/go-delve/delve/cmd/dlv@v1.27.1 \
 && go mod download

EXPOSE 8000 2345

CMD ["air", "-c", ".docker/air.toml"]
