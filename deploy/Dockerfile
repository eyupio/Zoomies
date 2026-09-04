# Zoomies controller and agent.
#
# One binary, two subcommands. Built static (CGO_ENABLED=0 -- the SQLite driver
# is pure Go) so the runtime image can be distroless.

# ---- UI ---------------------------------------------------------------------
FROM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Go ---------------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The UI build output is embedded by internal/api; it must be present before the
# Go build so that //go:embed finds it.
COPY --from=ui /src/internal/api/webdist ./internal/api/webdist
ARG VERSION=dev
ARG COMMIT=""
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/eyupio/zoomies/internal/version.Version=${VERSION} \
        -X github.com/eyupio/zoomies/internal/version.Commit=${COMMIT}" \
      -o /out/zoomies ./cmd/zoomies

# ---- Runtime ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS controller
COPY --from=build /out/zoomies /usr/local/bin/zoomies
# 65532 is distroless' "nonroot" user. The controller never needs root.
USER 65532:65532
VOLUME ["/var/lib/zoomies"]
EXPOSE 8080
ENV ZOOMIES_STATE_DIR=/var/lib/zoomies \
    ZOOMIES_CONFIG_DIR=/etc/zoomies \
    ZOOMIES_DB_PATH=/var/lib/zoomies/zoomies.db
ENTRYPOINT ["/usr/local/bin/zoomies"]
CMD ["controller"]

# The agent needs a shell only when it uses the process backend; for the
# container backends distroless is enough. It talks to the Docker or Podman
# socket, which is bind-mounted in.
FROM gcr.io/distroless/static-debian12:nonroot AS agent
COPY --from=build /out/zoomies /usr/local/bin/zoomies
USER 65532:65532
ENV ZOOMIES_STATE_DIR=/var/lib/zoomies
VOLUME ["/var/lib/zoomies"]
ENTRYPOINT ["/usr/local/bin/zoomies"]
CMD ["agent"]
