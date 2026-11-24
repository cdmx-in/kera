# Contributing

Thanks for helping. A few rules keep the project healthy.

## Sign your commits (DCO)

Every commit needs a `Signed-off-by:` line (`git commit -s`). It certifies the
[Developer Certificate of Origin](https://developercertificate.org/): you wrote
the change or have the right to submit it under the Apache-2.0 license.

## Clean-room rule for Open WebUI

Kera serves a subset of the Open WebUI HTTP API, so existing scripts and clients keep working, but it is not a fork.

- You may read Open WebUI to learn route paths, JSON field names, database column meanings and observable behaviour. Write what you learn in your own words in `docs/compat/openwebui-api.md`, and implement from that document.
- Do not copy code, comments, docstrings, prompt texts, CSS, tests or translations from any Open WebUI release newer than v0.6.5.
- Code ported from v0.6.5 or older (BSD-3-Clause) must start with the header below and be listed in `docs/compat/ports.md`:

  ```
  // Portions derived from Open WebUI v0.6.5 (BSD-3-Clause), Copyright (c) 2023-2025 Timothy Jaeryang Baek.
  // Upstream: <path> @ v0.6.5. See NOTICE.
  ```

## Pull requests

- Use a conventional-commit title (`feat:`, `fix:`, `docs:`, ...).
- Run `make test` before pushing. Database tests need `make testdb`.
- Keep deployment-specific names, URLs and prompts out of the core; they belong in your own configuration.
- Small, focused changes review fastest.
- Dependency PRs are welcome to review; Dependabot groups minor and patch updates weekly.

## Where to start

- Look for issues labelled [`good first issue`](https://github.com/cdmx-in/kera/labels/good%20first%20issue) or [`help wanted`](https://github.com/cdmx-in/kera/labels/help%20wanted).
- Documentation fixes are welcome: a wrong command, a missing step in a troubleshooting table, an out-of-date row in [docs/comparison.md](docs/comparison.md) (include a link to the source).
- Set up: `make testdb` starts the test Postgres, `make build` builds the web app and the binary, `make test` runs the Go and web tests. [AGENTS.md](AGENTS.md) describes the code layout: each feature is one package under `internal/`.
- For a larger change, open an issue or a [discussion](https://github.com/cdmx-in/kera/discussions) first so the approach can be agreed before you write it.
