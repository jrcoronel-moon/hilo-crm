# syntax=docker/dockerfile:1.7
# One build, three minimal images (targets): web, mcp, migrate.
#   docker build --target web --build-arg GIT_SHA=$(git rev-parse --short HEAD) .

# ───── build ─────
FROM golang:1.27.4-alpine AS build
WORKDIR /src
# Dependencies in their own layer: code changes do not re-download them.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
ARG GIT_SHA=dev
# No `go mod tidy` here: the image must contain exactly the dependencies that
# were tested and committed.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.gitSHA=${GIT_SHA} -X main.buildTime=$(date -u +%Y-%m-%dT%H:%MZ)" \
      -o /out/ ./cmd/...

# ───── runtime base: no shell, no package manager, non-root ─────
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
USER nonroot:nonroot
ENV NO_COLOR=

FROM runtime AS web
COPY --from=build /out/web /out/hiloctl /usr/local/bin/
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=4s --retries=3 CMD ["/usr/local/bin/web", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/web"]

FROM runtime AS mcp
COPY --from=build /out/mcp /usr/local/bin/
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=4s --retries=3 CMD ["/usr/local/bin/mcp", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/mcp"]

FROM runtime AS migrate
COPY --from=build /out/migrate /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/migrate"]
