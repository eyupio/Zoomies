# Zoomies controller and agent.
#
# One binary, two subcommands. Built static (CGO_ENABLED=0 -- the SQLite driver
# is pure Go) so the runtime image can be distroless.

# ---- UI ---------------------------------------------------------------------
# UI assets are architecture-independent; build them once on the builder's CPU.
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:0b36e8c136b94cd4fcf02188228e76c31ad5872eef3fec8cbd2eee500cfd9e80 AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
# No `|| npm install` fallback: that turns a lockfile CI would reject into an
# image built from whatever npm resolved today.
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Go ---------------------------------------------------------------------
# Go cross-compiles on the builder's CPU instead of running the compiler in QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The UI build output is embedded by internal/api; it must be present before the
# Go build so that //go:embed finds it.
COPY --from=ui /src/internal/api/webdist ./internal/api/webdist
# VERSION is the release without its leading v, the same string `make dist`
# stamps into the binaries: one release must not report two versions depending
# on how it was installed. DATE is passed because -trimpath and a build context
# with no .git leave Go nothing to infer one from.
ARG VERSION=dev
ARG COMMIT=""
ARG DATE=""
# Runner tags are separate from VERSION (main-sha-* is not a runner tag).
# Keep this a binary default, not ENV ZOOMIES_RUNNER_IMAGE: a baked environment
# override would incorrectly take precedence over the operator's YAML setting.
ARG RUNNER_IMAGE_TAG=dev
# Redeclare BuildKit's automatic target arguments without overriding their values.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/eyupio/zoomies/internal/version.Version=${VERSION} \
        -X github.com/eyupio/zoomies/internal/version.Commit=${COMMIT} \
        -X github.com/eyupio/zoomies/internal/version.Date=${DATE} \
        -X github.com/eyupio/zoomies/internal/naming.RunnerImageTag=${RUNNER_IMAGE_TAG}" \
      -o /out/zoomies ./cmd/zoomies

# An empty state directory to copy into the runtime stages. distroless has no
# shell, so this is the only way to get /var/lib/zoomies into the image owned by
# the user that runs there.
RUN install -d -m 0750 /out/state

# ---- Runtime ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS controller
# Re-declared after FROM: the ones set above are out of scope here, and a label
# written from an ARG that is not in scope is silently empty.
ARG VERSION=dev
ARG COMMIT=""
ARG DATE=""
# The image had no labels at all, so a registry listing said nothing about what
# it was or where it came from, and `docker inspect` could not answer which
# release a running container was. The binary inside has always known; this is
# the same fact where a person can reach it without starting anything.
LABEL org.opencontainers.image.title="zoomies" \
      org.opencontainers.image.description="Self-hosted GitHub Actions runner fleet controller" \
      org.opencontainers.image.source="https://github.com/eyupio/zoomies" \
      org.opencontainers.image.url="https://zoomies.sh" \
      org.opencontainers.image.documentation="https://zoomies.sh/quickstart/" \
      org.opencontainers.image.vendor="EyUp.io" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"
COPY --from=build /out/zoomies /usr/local/bin/zoomies
# The state directory must exist in the image, owned by the user that runs. A
# VOLUME on a path the image does not have gets created by the container runtime
# as root, and then uid 65532 cannot create the database in it -- SQLite reports
# only "unable to open database file (14)". Docker seeds a fresh named volume
# from the image, so baking the ownership in here is what makes that volume
# writable.
COPY --from=build --chown=65532:65532 /out/state /var/lib/zoomies
# 65532 is distroless' "nonroot" user. The controller never needs root.
USER 65532:65532
VOLUME ["/var/lib/zoomies"]
EXPOSE 8080
ENV ZOOMIES_STATE_DIR=/var/lib/zoomies \
    ZOOMIES_CONFIG_DIR=/etc/zoomies \
    ZOOMIES_DB_PATH=/var/lib/zoomies/zoomies.db
# The check the compose files already spell out, declared here so that a plain
# `docker run` gets it too -- an unhealthy controller that still has a running
# container is exactly the state an operator finds out about by accident.
#
# Exec form: distroless has no shell, so the string form docker would otherwise
# wrap in /bin/sh could never run. The port is the one EXPOSE names and the
# installer's ContainerPort constant uses; a controller told to listen
# elsewhere overrides this with its own --health-cmd.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/zoomies", "healthcheck", "--url", "http://127.0.0.1:8080"]
ENTRYPOINT ["/usr/local/bin/zoomies"]
CMD ["controller"]

# The agent needs a shell only when it uses the process backend; for the
# container backends distroless is enough. It talks to the Docker or Podman
# socket, which is bind-mounted in.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS agent
# Re-declared after FROM for the same reason the controller's are: the ARGs set
# in the build stage are out of scope here, and a label written from one that is
# not in scope is silently empty.
ARG VERSION=dev
ARG COMMIT=""
ARG DATE=""
# This target went unpublished for as long as it has existed, so nothing ever
# noticed it had no labels. Now that it is published it makes the same promise
# every other image here makes -- say what you are without being started.
LABEL org.opencontainers.image.title="zoomies-agent" \
      org.opencontainers.image.description="Zoomies agent: runs a runner fleet on one host for a controller" \
      org.opencontainers.image.source="https://github.com/eyupio/zoomies" \
      org.opencontainers.image.url="https://zoomies.sh" \
      org.opencontainers.image.documentation="https://zoomies.sh/hosts/" \
      org.opencontainers.image.vendor="EyUp.io" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"
COPY --from=build /out/zoomies /usr/local/bin/zoomies
COPY --from=build --chown=65532:65532 /out/state /var/lib/zoomies
USER 65532:65532
ENV ZOOMIES_STATE_DIR=/var/lib/zoomies
VOLUME ["/var/lib/zoomies"]
ENTRYPOINT ["/usr/local/bin/zoomies"]
CMD ["agent"]
