# Contributing

Thanks for helping. A few rules keep the project healthy.

## Sign your commits (DCO)

Every commit needs a `Signed-off-by:` line (`git commit -s`). It certifies the
[Developer Certificate of Origin](https://developercertificate.org/): you wrote
the change or have the right to submit it under the Apache-2.0 license.

## Clean-room rule for Open WebUI

Kera offers an Open WebUI-compatible API and an importer, but it is not a fork.

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
