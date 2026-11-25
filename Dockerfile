# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH
# the build runs on the builder's own architecture and cross-compiles, so a multi-arch image needs no emulation
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X github.com/cdmx-in/kera/internal/version.Version=${VERSION}" -o /kera ./cmd/kera
RUN mkdir -p /out/data /out/run/kera-runner

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /kera /usr/local/bin/kera
# /data must belong to the runtime user; a new named volume copies this ownership.
COPY --from=go --chown=65532:65532 /out/data /data
# where Kera keeps the token of the built-in pipeline runner (KERA_RUNNER_TOKEN_FILE); the runner container reads the same volume
COPY --from=go --chown=65532:65532 /out/run/kera-runner /run/kera-runner
ENV KERA_LISTEN=:8080 KERA_DATA_DIR=/data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/kera"]
CMD ["serve"]
