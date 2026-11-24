# Kera compared with other self-hosted AI chats

Checked on 2026-11-24 against each project's own documentation and repository. These projects ship weekly, so treat the table as a snapshot. If a cell is wrong or out of date, please open an issue or a pull request with a link; corrections are welcome and will be applied quickly.

"Not checked" means we did not verify the cell. It does not mean the feature is absent.

| | Kera | Open WebUI | LibreChat | AnythingLLM | Onyx (Community) |
|---|---|---|---|---|---|
| Licence | Apache-2.0 | Open WebUI License: BSD-3-Clause terms plus a branding clause (rebranding is allowed up to 50 users in 30 days, or with written permission or an enterprise licence); contributors sign a CLA. Code up to v0.6.5 is plain BSD-3-Clause | MIT | MIT | MIT outside `ee/`; `ee/` is under the Onyx Enterprise License |
| Services in the official compose file | 2: the app and Postgres with pgvector. Optional profiles add Ollama and LiteLLM | 1: the app, with an embedded SQLite database and vector store. Postgres, Redis and an external vector database are for running several replicas | App, admin panel, MongoDB, Meilisearch, a pgvector database and a RAG API | 1 | About 14, including OpenSearch, Redis and MinIO. A Lite mode needs under 1 GB of memory but does not index documents |
| Scheduled agent tasks | Cowork scheduled tasks; on for admins, opt-in for other users | Automations (since v0.9.0) | Scheduled Chats (experimental) | Scheduled Jobs, single-user mode only | Not checked |
| Built-in personal-data redaction | Yes, on every model call (redact or block), with an audit trail | None found in the docs; custom filter functions | None found in the docs; rate limits and violation scores | Not checked | Not checked |
| Per-user usage or spend | Admin > Spend, one row per person, from LiteLLM; admin analytics | Admin analytics with token usage and per-user activity; per-user cost not documented | Per-user token balances | Not checked | Usage analytics and per-user limits in the paid editions |
| Desktop app | No | Yes, since v0.9.0 | None documented | Yes | Yes |
| Interface languages | English only | 64 locale folders in the repository | 44 locale folders in the repository | Not checked | Not checked |

Rows left out because every project in the table does the same: retrieval over uploaded files, MCP tool servers, OIDC sign-in, code execution. The differences that matter in practice:

- **Footprint.** Open WebUI and AnythingLLM are one container and start with less than Kera does. Kera asks for Postgres and gets a multi-user database from the first start, with no Redis, vector database or queue added later. LibreChat and Onyx run several services.
- **Licence.** Kera is Apache-2.0 without a branding clause or tenant limits. Open WebUI's licence allows more than 50 users to run it as is, but not to change its branding without permission.
- **In the base install.** Guardrails with an audit trail, per-user spend through LiteLLM, knowledge Pipelines with a flow view, and configuration as code (`kera apply`, `kera export`).
- **Where Kera is behind.** The ecosystem and community are much smaller, there is no desktop or mobile app, the interface is not translated, and sign-in is email and password, Google or OIDC only (no LDAP, SAML or SCIM). If those matter more than the points above, one of the other projects is the better choice.

Dify and LobeHub are broader application platforms. They are not in the table because several cells could not be verified. Mattermost has an AI plugin (Agents), which is a different kind of product (a chat platform with an assistant) and is not compared here.

## Sources

- Open WebUI: [LICENSE](https://github.com/open-webui/open-webui/blob/main/LICENSE), [licence page](https://docs.openwebui.com/license/), [scaling guide](https://docs.openwebui.com/getting-started/advanced-topics/scaling), [docker-compose.yaml](https://github.com/open-webui/open-webui/blob/main/docker-compose.yaml), [automations](https://docs.openwebui.com/features/chat-conversations/chat-features/automations), [v0.9.0 release](https://github.com/open-webui/open-webui/releases/tag/v0.9.0), [analytics](https://docs.openwebui.com/features/administration/analytics), [locales](https://github.com/open-webui/open-webui/tree/main/src/lib/i18n/locales).
- LibreChat: [LICENSE](https://github.com/LibreChat-AI/LibreChat/blob/main/LICENSE), [docker-compose.yml](https://github.com/LibreChat-AI/LibreChat/blob/main/docker-compose.yml), [features docs](https://www.librechat.ai/docs/features), [token balances](https://www.librechat.ai/docs/configuration/librechat_yaml/object_structure/balance), [locales](https://github.com/LibreChat-AI/LibreChat/tree/main/client/src/locales).
- AnythingLLM: [README and LICENSE](https://github.com/Mintplex-Labs/anything-llm), [docker-compose.yml](https://github.com/Mintplex-Labs/anything-llm/blob/master/docker/docker-compose.yml), [scheduled jobs](https://docs.anythingllm.com/scheduled-jobs/overview).
- Onyx: [README and LICENSE](https://github.com/onyx-dot-app/onyx), [docker-compose.yml](https://github.com/onyx-dot-app/onyx/blob/main/deployment/docker_compose/docker-compose.yml), [editions](https://docs.onyx.app/admins/getting_started/enterprise_edition).
- Kera: this repository ([LICENSE](../LICENSE), [deploy/docker-compose.yml](../deploy/docker-compose.yml), [guardrails](../README.md#guardrails), [LiteLLM and spend](litellm.md)).
