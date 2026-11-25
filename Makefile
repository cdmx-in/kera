VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/cdmx-in/kera/internal/version.Version=$(VERSION)
export KERA_TEST_DATABASE_URL ?= postgres://kera:kera-test@127.0.0.1:55432/kera?sslmode=disable

# pnpm when it is installed (CI, developer machines); otherwise the binaries already in web/node_modules/.bin
# (a host that only has node_modules). The commands match the scripts in web/package.json.
ifneq ($(shell command -v pnpm 2>/dev/null),)
WEB_INSTALL := pnpm install --frozen-lockfile
WEB_TYPECHECK := pnpm typecheck
WEB_TEST := pnpm test
WEB_BUILD := pnpm build
else
WEB_INSTALL := true
WEB_TYPECHECK := node_modules/.bin/tsc --noEmit
WEB_TEST := node_modules/.bin/vitest run --passWithNoTests
WEB_BUILD := node_modules/.bin/tsc --noEmit && rm -rf dist/assets && node_modules/.bin/vite build
endif

.PHONY: all web build test test-go test-web lint testdb image clean

all: build

web:
	cd web && $(WEB_INSTALL) && $(WEB_BUILD)

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o kera ./cmd/kera

test: test-go test-web

# coverage.out (gitignored) is the profile CI uploads; DB tests count when KERA_TEST_DATABASE_URL is set.
test-go:
	go vet ./... && go test -race -coverprofile=coverage.out ./...

test-web:
	cd web && $(WEB_TYPECHECK) && $(WEB_TEST)

# Fast checks with no extra tools: gofmt must list nothing, go vet, the web typecheck.
lint:
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed (run: gofmt -w <file>):"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	cd web && $(WEB_TYPECHECK)

# Local Postgres 18 + pgvector for tests (localhost only).
testdb:
	docker run -d --name kera-test-db --restart unless-stopped -e POSTGRES_USER=kera -e POSTGRES_PASSWORD=kera-test \
	  -e POSTGRES_DB=kera -p 127.0.0.1:55432:5432 pgvector/pgvector:pg18

image:
	docker build --build-arg VERSION=$(VERSION) -t ghcr.io/cdmx-in/kera:$(VERSION) .

clean:
	rm -f kera; find web/dist -mindepth 1 ! -name .gitkeep -delete
