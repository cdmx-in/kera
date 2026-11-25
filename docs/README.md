# Kera documentation

| Page | Read it to |
|---|---|
| [deploy.md](deploy.md) | Run Kera with Docker Compose: first start, signing in, models, config as code, HTTPS, backups, troubleshooting |
| [litellm.md](litellm.md) | Put LiteLLM in front of your models and get per-user spend in Admin > Spend |
| [pipelines.md](pipelines.md) | Keep a knowledge collection in sync with a folder, on a schedule, with a flow view of each run |
| [how-it-answers.md](how-it-answers.md) | See how a question is answered, from guardrails to retrieval to the tool loop, as diagrams with the settings that steer each step and where to look when one misbehaves (also in Admin > Help) |
| [run-trace.md](run-trace.md) | See what each model round and tool call of a run cost (Admin > Traces), and choose where retrieved passages go in the prompt |
| [context-management.md](context-management.md) | Understand how a run stays inside the model's window, and how it recovers from failed calls, repeated calls and the round cap |
| [integrations.md](integrations.md) | Scheduled directory sync from Google Workspace, and how to write another integration |
| [retrieval.md](retrieval.md) | Understand how knowledge search ranks and returns passages, which settings change it, and what an upgrade needs (`kera reindex --heads`, prefixes) |
| [audit.md](audit.md) | See what the audit log records and leaves out, how long it keeps events, and how to export them |
| [comparison.md](comparison.md) | See how Kera differs from Open WebUI, LibreChat, AnythingLLM and Onyx, with sources and a date |
| [compat/openwebui-api.md](compat/openwebui-api.md) | Look up the HTTP routes Kera serves, including the Open WebUI-compatible subset |

Helper services have their own READMEs: [document extraction](../sidecars/extract/README.md) (part of the Compose stack), [reranking](../sidecars/rerank/README.md) and [Cowork desktop](../sidecars/desktop/README.md) (optional).

For contributors: [CONTRIBUTING.md](../CONTRIBUTING.md) (rules and places to start), [AGENTS.md](../AGENTS.md) (code layout and conventions, for people and coding agents), [SECURITY.md](../SECURITY.md).
