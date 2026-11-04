# syntax=docker/dockerfile:1
FROM node:24-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/cdmx-in/kera/internal/version.Version=${VERSION}" -o /kera ./cmd/kera

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /kera /usr/local/bin/kera
ENV KERA_LISTEN=:8080 KERA_DATA_DIR=/data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/kera"]
CMD ["serve"]
