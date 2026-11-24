# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
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
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /kera /usr/local/bin/kera
# /data must belong to the runtime user; a new named volume copies this ownership.
COPY --from=go --chown=65532:65532 /out/data /data
ENV KERA_LISTEN=:8080 KERA_DATA_DIR=/data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/kera"]
CMD ["serve"]
