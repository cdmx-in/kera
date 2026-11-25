# Audit log

Kera keeps a record of who did what, when, and to what: sign-ins, changes made in the admin console, stored secrets, pipelines, sharing. Admins read it under **Admin > Audit**, one plain sentence per event ("ann@example.com replaced the value of the stored secret read-token · 10:32 today"). Open an event to see its structured record: the action, the actor, the target, the outcome, the client's address and user agent, and a few names that say what changed.

The guardrail trail (what redaction did to model calls, under Admin > Settings > Guardrails) is separate and stays as it was.

## What is recorded

Every event has a time, an actor (a user, an API key, an integration, the `kera` command line, or "someone" not signed in), a stable action name, a target (type, id and its name at the time), an outcome (`ok`, `denied` or `failed`), the client address and user agent, and small structured details.

- **Every admin API call that can change something** (POST, PUT, PATCH or DELETE on a route that requires an admin), whether it worked, was refused (`denied`: a signed-in non-admin tried) or failed (a 4xx or 5xx answer). Calls that do not have a name of their own are recorded as `admin.request` with their method and route.
- **Sign-in**: `auth.sign_in`, `auth.sign_in_failed` (password and OAuth, with the reason), `auth.sign_out`, `auth.password_change`, `auth.login_link` (`kera user login-link`).
- **Users and access**: `user.create`, `user.role_change` (from and to), `user.rename`, `user.password_set`, `user.disable` and `user.enable` (directory sync), `user.delete`, `permissions.update`, `group.create`, `group.update`, `group.member_add`, `group.member_remove`, `group.delete`, `access.update` (grants on a model, collection, tool server ...), `apikey.create`, `apikey.revoke`.
- **Stored secrets**: `secret.create`, `secret.update` (which of value and description changed), `secret.delete`.
- **Pipelines and runners**: `pipeline.create`, `.update`, `.delete`, `.run`, `.run_cancel`, `.run_close`, `.script_save`, `.schedule_change`, `.stages_update`, `.env_set` (variable name, whether it is write-only, which stored secret it points at), `.env_delete`, `.token_rotate`, `.override`, `.check`, the notebook actions, `runner.create`, `runner.token_rotate`, `runner.delete`.
- **Settings and services**: `settings.update` (the names of the settings), `guardrails.update` (a save of guardrail settings only), tool servers, hook servers, sign-in providers, model connections, skills, brand images, `integration.run`, `model.create`, `.update`, `.toggle`, `.delete`, and `config.apply` (`kera apply`, with counts per change).
- **Data**: `knowledge.delete`, `knowledge.file_remove`, `chat.share`, `chat.unshare`, `memory.clear`, `config.export` (`kera export`), `audit.export`.

The full list, with the sentence each one becomes, is in `web/src/features/admin/audit.ts`; the admin routes and their names are in `internal/audit/actions.go`.

## What is not recorded

- **No values.** Never the value of a secret, token, password, API key or environment variable, and never message, prompt or file content. Details hold names and flags only ("changed: value", "settings: site.name"). As a safety net, a detail whose name ends in a word like password, token, key, secret, value, content or message is dropped before it is stored, and a string that looks like a Kera credential (`sk-`, `kp-`, `kr-`, `Bearer `) is replaced.
- **No reading.** GET requests, chat traffic, streaming, health checks and polling leave no event.
- **No noise from attackers.** A sign-in refused by the sign-in lock (HTTP 429) is not recorded, a request without any credential to an admin route (401) is not recorded, and refused or failed events are capped at 30 per client address and 600 in all per minute; what goes over the cap is dropped, with one log line saying so.
- **Not everything a user does.** Ordinary chat actions (new chats, renames, folders, tags, uploads) are not audit events. Scheduled pipeline runs and integration runs have their own run history; only the user accounts an integration creates, disables, enables or deletes are recorded.
- **Chat titles** are content and are left out: a shared chat is named by its id.

## Retention

Events are kept for 365 days by default. Change it under Admin > Audit ("Keep events for N days"), with the setting `audit.retention_days`, or in the config file:

```yaml
audit:
  retention_days: 730   # 0 keeps every event
```

A purge runs a minute after the server starts and then once a day, and deletes events older than that. The app never edits or deletes an event otherwise: there is no route for it.

## Export

**Export CSV** on the Audit page downloads the events that match the current filters, newest first, at most 10,000 rows. The same file comes from the API:

```sh
curl -H "Authorization: Bearer $KERA_ADMIN_KEY" \
  "https://kera.example.com/api/v1/admin/audit/export?action=secret.,pipeline.env&from=2026-01-01" -o audit.csv
```

`GET /api/v1/admin/audit` returns the same events as JSON, 50 a page (`limit` up to 200), with a `next` cursor. Filters, for both: `actor` (a user id, or text in the email or name; it also returns the `auth.sign_in_failed` events against that user, see below), `action` (comma-separated prefixes such as `auth.,user.`), `target` (an id, or text in the name), `outcome` (`ok`, `denied`, `failed`, comma-separated), `from` and `to` (RFC 3339 times or `YYYY-MM-DD` days, `to` inclusive) and `q` (text anywhere). Both routes are admin only, and every export is itself recorded. A cell that a spreadsheet would read as a formula is prefixed with `'`.

## Privacy

The log holds personal data: email addresses, names, client addresses and user agents. Only admins can read it. Keep the retention no longer than you need, and include the log in what you tell your users about the data Kera keeps. Deleting a user keeps their past events (with the email they had at the time), which is the point of an audit log; to remove them too, delete their rows from `audit_event` in the database.

From the Users page, the **Activity** button next to a user opens the Audit tab filtered to what that user did. A failed sign-in is stored with an anonymous actor ("someone") and the attempted account as its target, because nobody was signed in. The `actor` filter therefore also returns the `auth.sign_in_failed` events whose target is that user (by id, or by the email that was tried, in any letter case), so the Activity view shows the failed attempts against the account too. This changes only what the filter returns, not what is stored; other events that merely have the user as their target (an admin changing their role, for example) are not part of their activity.
