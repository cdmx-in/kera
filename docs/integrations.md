# Integrations

Integrations are scheduled syncs with outside systems. Each one lives in its own package under `internal/connectors/` and registers itself with `connectors.Register` from `init()`. The `connectors` package:

- runs every enabled integration on its interval;
- runs one integration at a time, guarded by an in-process flag plus a Postgres advisory lock, so the scheduler, the admin button and the CLI never overlap;
- records every run, dry runs included, in `connector_run` together with its report: counts, changes and warnings.

Every integration has two settings:

| Setting | Default | Meaning |
|---|---|---|
| `connectors.<name>.enabled` | `false` | run on a schedule |
| `connectors.<name>.interval_minutes` | `60` | minutes between the starts of two scheduled runs (at least 5) |

The scheduler reads both every minute, so a change applies without a restart. A run that fails still counts as a run, so the next attempt waits one interval. Dry runs do not count.

Ways to run an integration:

- Admin console: **Admin > Settings > Integrations** has **Dry run** and **Sync now** buttons and lists the last 20 runs.
- CLI: `kera connectors list` and `kera connectors run NAME [-dry-run]`. The CLI prints the report as JSON.
- API: `GET /api/v1/admin/connectors` and `POST /api/v1/admin/connectors/{name}/run[?dry_run=1]`.

To add an integration, write a package with a `Run(ctx, *app.App, dryRun) (*connectors.Report, error)` that makes no changes when `dryRun` is set. Add its settings to `settings.Defaults` and to `apply.Connectors`, add a form to `Integrations.tsx`, and add a blank import of the package in `cmd/kera`.

## Google Workspace (`google_workspace`)

Reads users from the Admin SDK Directory API. It authenticates as a service account with domain-wide delegation, acting as a Workspace admin, with the read-only scope `https://www.googleapis.com/auth/admin.directory.user.readonly`. All changes of a run happen in one transaction; a dry run rolls it back.

| Workspace | Kera |
|---|---|
| Active user with no Kera account | A user is created with `origin = gworkspace` and the default sign-up role (`admin` becomes `user`). The account is linked to the Google login: `oauth_sub = <oauth_provider>@<directory id>`, and the directory id is the OIDC `sub`. |
| User whose email matches a Kera account without a login | The account is linked to the login. An email that already signs in with another identity is left alone, with a warning. |
| Name or primary email changed | Updated. The user is matched by the directory id, so a rename keeps the account. |
| Suspended or archived | Disabled: `disabled_at` is set. Sessions and API keys stop working at once, and sign-in fails with `account_disabled`. |
| Active again | Enabled. |
| No longer listed (deleted) | Kera users whose email domain is one of the Workspace's domains are disabled at once and deleted after `delete_after_days`. Deletion removes their chats, files, knowledge and memory. If they come back before that, the countdown stops. |
| Department (primary organization, else the first one with a department) | Membership of the Kera group with that name. The group is created if needed, and an existing group with the same name is reused. The sync only removes memberships it added itself (`group_member.origin = gworkspace`), so memberships added by hand stay. A group whose department empties is kept, together with its access grants. |

Safety rules:

- An empty listing aborts the run.
- If more users would be newly disabled as missing than `max(5, 10% of listed users)`, the run aborts and changes nothing.
- The last active admin is never disabled or deleted.
- Emails in `exclude` are never touched.

| Setting | Default | |
|---|---|---|
| `connectors.google_workspace.credentials` | — | secret; the service-account JSON key |
| `connectors.google_workspace.admin_email` | `""` | the Workspace admin the service account acts as |
| `connectors.google_workspace.oauth_provider` | `"google"` | id of the Google sign-in provider |
| `connectors.google_workspace.exclude` | `[]` | emails to leave alone (bots, break-glass accounts) |
| `connectors.google_workspace.delete_after_days` | `30` | grace period before a deleted user is removed |

In `kera apply` YAML:

```yaml
connectors:
  google_workspace:
    enabled: true
    interval_minutes: 60
    credentials: ${KERA_GOOGLE_WORKSPACE_KEY}
    admin_email: admin@example.com
    exclude: [bot@example.com]
```

Setup:

1. In Google Cloud, create a service account and a JSON key for it. A dedicated account that holds only this scope is best.
2. In the Workspace Admin console, go to **Security > Access and data control > API controls > Domain-wide delegation** and add the service account's client id with the scope above.
3. In Kera, go to **Admin > Settings > Integrations**. Paste the key, set the admin email and the exclusions, then click **Dry run**. Read the report, especially the `disable` lines. Only then switch the integration on.
