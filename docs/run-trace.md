# Run trace and prompt layout

Two settings that help you see what a run costs and keep a cache-friendly prompt.

## The run trace

Every chat, API and task run (including scheduled and Cowork runs: they share one loop) writes a trace when it finishes: one row per model round, tool call, knowledge retrieval and compaction, in the `run_step` table. Admins read it under **Admin > Traces**: a list of recent runs (rounds, tool calls, tokens in and out, cached share, time), and for one run a waterfall of its steps on a shared time axis.

A step records:

| Step | What is kept |
|---|---|
| model round | the model, duration, prompt, completion and cached prompt tokens (cached only when the server reports them: OpenAI-compatible `prompt_tokens_details.cached_tokens` and Responses `input_tokens_details.cached_tokens`; Ollama does not), the stop reason (`stop`, `tool_calls`, `length`), a `truncated` flag for `length`, messages and characters sent, tools offered and the size of their definitions, characters of text and number of tool calls the model returned |
| tool call | the tool name and server, characters of arguments and of the result, `is_error`, `truncated` (set when a tool server cut the result at its size cap), files and images returned |
| retrieval | collections searched, whole documents added, passages found, the top scores, whether the search was reranked |
| compaction | turns summarised, estimated tokens before and after. Context management adds steps of this kind inside a run: `clear`, `truncate`, `summarise_run`, `spill`, `overflow_suspected`, `too_large` and `overflow_retry` (see [context-management.md](context-management.md)) |

Model calls that were retried, and the closing report at the tool-round cap (stop reason `round_cap`), are recorded as their own model steps; see [context-management.md](context-management.md).

A failed model call stores a short code (`provider_error`, `timeout`, `cancelled`...) and the HTTP status, never the server's message.

**What is never stored:** prompts, answers, tool arguments, tool results (not even a preview), search queries, or any secret. A trace holds sizes, counts, timings and flags, so it is not encrypted and is not part of `kera secrets rotate`. Background model calls (titles, tags, follow-ups, memory review, search-query rewriting) are not traced; they appear in the usage totals only.

Writing is cheap and best effort: steps are collected in memory (at most 2,000 per run) and inserted in one batch after the answer is saved. If the insert fails, a warning is logged and the chat is not affected.

### Settings

| Key | Default | Meaning |
|---|---|---|
| `trace.enable` | `true` | Record traces. |
| `trace.retention_days` | `30` | Days a trace is kept. An hourly job deletes older rows (at least 1 day is always kept). |

Both are in Admin > Settings > Chat.

### API

Admin only (403 for other users):

- `GET /api/v1/admin/runs?days=7&limit=100` lists the newest traced runs with their totals. `days` is 1 to 366, `limit` up to 500.
- `GET /api/v1/admin/runs/{message_id}/steps` returns the steps of the newest run of that assistant message (a regenerated answer keeps its message id), 404 when there is none.

Questions it answers in SQL: prompt size per round by model (`prompt_tokens`), cached share (`cached_tokens / prompt_tokens`), tool error rate (`detail->>'is_error'`), how often a result is large (`detail->>'result_chars'`), how many tool definitions a preset sends (`detail->>'tools_offered'`).

## Where retrieved passages go

`rag.placement` (Admin > Settings > Retrieval) decides where knowledge passages are put in the prompt:

- `system` (default): inside the system prompt, as before.
- `data`: the system prompt keeps only the instructions (answer from the sources, cite by number, say so when they lack the answer, treat them as data), and the passages become their own message just before the newest user message, marked as untrusted reference material. They are built per run and are not saved with the chat.

With `data`:

- The guardrails treat that message like tool output: personal data is logged, redacted or blocked per `guardrails.pii`. An injection phrase in it is only counted, under the rule `injection.data` (log action); it never blocks the user's own message, and the blocklist and moderation do not read it. In `system` placement the guard skips the system prompt, so passages are not scanned.
- The system prompt no longer changes with the question, so providers that cache the prompt prefix can reuse it across turns. Compare the cached share of follow-up turns in Admin > Traces before and after.
- Citations work the same: passages are numbered `[1]`, `[2]`... and the answer's sources list is unchanged.
- A custom `rag.template` keeps its instructions in the system prompt, with a one-line pointer to the reference message where `{{CONTEXT}}` was.
- Some local chat templates expect strict user/assistant alternation. The reference message adds a second user message in a row (scheduled runs already do this); check each local model once before switching.

`system` stays the default until you have compared answers on your own questions under both placements.

For prompt authors: `{{CURRENT_DATE}}` is stable all day; `{{CURRENT_TIME}}` and `{{CURRENT_DATETIME}}` change the start of the prompt on every call and defeat prefix caching. Prefer the date.
