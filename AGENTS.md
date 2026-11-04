# Working on Kera (humans and coding agents)

- Go module `github.com/cdmx-in/kera`, Go 1.26, `CGO_ENABLED=0`. HTTP is stdlib `net/http` (1.22 patterns). Database is pgx/v5 with hand-written SQL; schema migrations are goose SQL files in `internal/db/migrations`. No ORM, no JWT or UUID libraries (see `internal/auth`, `internal/ids`).
- A feature lives in its own package under `internal/` and exposes `Mount(a *app.App) error`, listed once in `internal/app/mounts.go`. Use `a.Auth.Required/Admin/Permission` for access, `auth.FromContext` for the user, `api.JSON/Fail/Decode/Errf` for responses, `a.Settings` for runtime config, `a.Keys` to encrypt secrets at rest, `ids.New()` for ids.
- Frontend: `web/` React 19 + Vite + TypeScript + Tailwind 4. Build output `web/dist` is embedded into the binary.
- Environment: `KERA_DATABASE_URL`, `KERA_SECRET_KEY`, `KERA_URL`, `KERA_DATA_DIR`, `KERA_LISTEN`, `OLLAMA_BASE_URL`.
- Ids are `text` (UUID v7 from Go; imported ids are kept as they are). Timestamps are `timestamptz`; Open WebUI-compatible routes return epoch seconds.
- Tests sit next to the code. Database tests call `testdb.New(t)`, which gives each test a fresh migrated database and skips when `KERA_TEST_DATABASE_URL` is unset (`make testdb` starts one).
- Follow CONTRIBUTING.md, especially the clean-room rule. Keep deployment-specific names, URLs and prompts out of the core (CI enforces this).
- Prefer boring code: small functions, errors wrapped with context. Mark a deliberate shortcut with a `ponytail:` comment that names its limit and the upgrade path.
