# harbor's container image: one image that serves the site (/app, the entrypoint) and runs
# the deployment's migration step (/migrate, the command the Cloud Run job is deployed with).
#
# Every base image is referenced by digest, never by tag alone: the build is reproducible,
# nothing updates underneath it, and a later security scan targets exactly what was deployed.
# To find a digest, pull the tag and inspect it:
#
#     docker pull cgr.dev/chainguard/go:latest
#     docker inspect cgr.dev/chainguard/go:latest --format='{{index .RepoDigests 0}}'
#
# Build arguments: VERSION (the release; the process reports it as APP_VERSION) and COMMIT
# (the commit it was built from, recorded on the image). A local build:
#
#     podman build --build-arg VERSION=dev --build-arg COMMIT=$(git rev-parse HEAD) -t harbor .
#
# The pipeline's build is cloudbuild.yaml's BuildImage step.

ARG VERSION=dev
ARG COMMIT=unknown

# ---- Go: the server and the migrate command ----------------------------------------------
# The go image is pinned at a digest that carries a toolchain at least as new as go.mod
# declares (go1.27.4, published 2026-11-11), found the way the header says. GOTOOLCHAIN=local
# makes a go.mod that outruns the image fail the build instead of downloading a toolchain:
# the fix is a new digest here, never a download.
FROM cgr.dev/chainguard/go@sha256:905d3bb3fbb15a801a974de607f66c7ba655a327dc40f0a1d42d4a24708165bd AS build-env

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

WORKDIR /go/src/app

# The module graph first, on its own layer, so a source change does not re-download it: a
# local build keeps the layer, and the pipeline's build reads the download from the cache
# it keeps in the registry per commit.
COPY go.mod go.sum ./
RUN go mod download

COPY . ./

# -trimpath keeps the build directory out of the binaries; -s -w drop the symbol table and
# the debug information, which nothing in a deployed image reads. No build tags: the
# session library's skipAuth tag (Procfile) simulates the directory and never ships.
RUN go build -trimpath -ldflags="-s -w" -o /build/app . && \
    go build -trimpath -ldflags="-s -w" -o /build/migrate ./cmd/deployment/migrate && \
    go build -trimpath -ldflags="-s -w" -o /build/jobs ./cmd/jobs

# ---- Console: the Angular bundle ---------------------------------------------------------
# bun installs the workspace from web/bun.lock and runs its scripts; Angular's CLI itself
# runs under Node, so the builder is a node image with bun's pinned binary copied in from
# bun's own image. Nothing else of that image is used.
FROM docker.io/oven/bun@sha256:3f23864d3e8c923975c61971cabe67ee3d5a728f1e25a559256b95af719a3478 AS bun-binary

FROM docker.io/library/node@sha256:9d266751ca3d7fdbe651c878ffd5ad0a42fed49a18c283509267c97f025b7496 AS console-build-env

COPY --from=bun-binary /usr/local/bin/bun /usr/local/bin/bun

# VERSION reaches this stage as an environment variable of the build, so the workspace's
# build script can stamp the release into each bundle and the browser can send it with
# its requests; a local build without the argument stamps dev. The Firebase web API key
# and the environment project, values the stack makes that placement.json's
# buildArguments declares, reach it the same way.
ARG VERSION
ARG FIREBASE_API_KEY
ARG PROJECT_ID
ENV CI=true

WORKDIR /src/web
COPY web/ ./

# --frozen-lockfile installs exactly what bun.lock records or fails: the lockfile is the
# build's input, never its output. `bun run build` is `ng build console`, whose default
# configuration is production (web/angular.json); it writes dist/console.
RUN bun install --frozen-lockfile && \
    bun run build

# ---- Runtime: a static image with the two binaries, the bundle, and the schema -----------
FROM cgr.dev/chainguard/static@sha256:939754fefdbdb198356dc3187310417777d4dd8561be000885e224d49a4ce727

ARG VERSION
ARG COMMIT
LABEL org.opencontainers.image.title="harbor" \
      org.opencontainers.image.source="https://github.com/impulseframework/harbor" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

COPY --from=build-env /build /

# The server serves the bundle from the directory APP_CONSOLE_DIST names (pkg/config, site
# level). The migrate command reads schema/migrations and schema/roles/staff.json relative
# to its working directory (pkg/deploy.MigrationsSource, staff.RolesPath), so both sit
# under the working directory set below.
COPY --from=console-build-env /src/web/dist/console /home/nonroot/web/dist/console
COPY schema/migrations /home/nonroot/schema/migrations
COPY schema/roles /home/nonroot/schema/roles

# APP_VERSION is the configuration's version input (pkg/config, core level): the image
# carries its own release, so no deploy step repeats it. PORT is Cloud Run's; the
# configuration's default, 8080, is what the image declares.
ENV APP_VERSION="${VERSION}" \
    APP_CONSOLE_DIST=/home/nonroot/web/dist/console \
    APP_PORTAL_DIST=/home/nonroot/portal/dist/portal

WORKDIR /home/nonroot
USER nonroot
EXPOSE 8080

ENTRYPOINT ["/app"]
