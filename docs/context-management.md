# Context management inside a run

A run that calls tools adds every result to the prompt, and a local model's window is small (Ollama starts at 4k to 32k tokens, depending on memory). This page describes what Kera does to keep a run inside the window, and how it recovers when a model call fails. Every step is recorded in the [run trace](run-trace.md), so you can measure it.

## What happens, in order

1. **The loop knows the size.** After each model call it keeps the prompt size the server reported (`prompt_tokens`) and adds an estimate (characters / 4) for the messages added since. Before the first report, after a rewrite, and when a server reports less than half of the estimate (some servers count only the uncached part), it uses the estimate alone. There is no tokenizer.
2. **The loop knows the window.** In this order: the model parameter `context_window`, the parameter `num_ctx`, then a setting: `chat.context.window_local` (16,000) for a local model, `chat.compaction.threshold` (120,000) for a hosted one. Set `context_window` in the preset's parameters (Workspace > Models) for any model whose real window you know. When a call reports 97% or more of the window, the trace gets an `overflow_suspected` step: Ollama cuts the oldest messages of a prompt that is too long and still answers with success, so this is the only sign.
3. **Long results become files.** A tool result longer than `tools.spill_chars` (0 = one eighth of the window, at least 8,000 characters; -1 = never) is stored as a file of the user (`<tool>-result-<id>.txt`; lines over 500 characters are wrapped). The model gets the first 2,000 and last 1,000 characters and one line with the size, the file name and id, and the hint to read the rest with `view_file` or `grep_chat_files`. The file is an input of the step in the chat (shown on the step, never attached to the answer), joins the run's readable files, and in a saved chat is linked to the chat, so the next turn can list and read it. The file tools join a run whenever it has tools other than the core set, even without attachments. The results of the file tools themselves are never spilled, and `view_file` pages stop at 30,000 characters. Spilled files stay when the chat is deleted (like tool uploads); remove them by `meta.source = "spill"` if storage matters.
4. **Old results are cleared.** Before a model call, when the size is over `chat.context.clear_at` (0.7) of the window, the content of tool results older than the newest `chat.context.keep_rounds` (4) rounds is replaced by one line (`[cleared to save context: <tool> returned N characters]`, plus the file name when the result was spilled), oldest first, until the size is down to `chat.context.clear_to` (0.5) or nothing older is left. It is one batch, so the prompt prefix changes rarely. A cleared result keeps its message: every tool call still has its result. The system prompt, earlier turns and the user's request are never touched.
5. **The run so far is summarised.** If the size is still over `clear_at`, and `chat.context.compact` is on, the run's own messages before the newest 2 rounds are condensed by the task model with the same summary prompt used between turns (plus a line asking to keep what was found, tried and still open). The summary replaces them as one message, with the names of any spilled files. Cuts fall on round boundaries. If the summary call fails the run continues: the loop clears with only 2 rounds kept. There is no plan tool yet, so no plan message is carried over.
6. **Between turns.** Compaction (`chat.compaction.enable`, now on by default) counts the system prompt, retrieved sources and tool definitions toward its threshold, and runs once the prompt and tools are built. The threshold is `compact_token_threshold` from the model parameters if set, else 70% of the window when the model declares one or is local, else `chat.compaction.threshold`. When `chat.context.digest` is on, an earlier assistant reply that used tools is sent with a short list of them (below).

## The digest of earlier steps

Earlier turns are replayed as text only, so a follow-up such as "use the second source" lost what the reply did. With the digest, an assistant turn that made tool calls ends with:

```
[Tool calls made for the reply above:
- search_web {"query":"rates"} -> first line of the result ...
- view_file {"file":"f"} -> failed: no such file]
```

- It is built from the message's stored steps, not from the database of tool results: the stored preview is already limited to 2,000 characters, and the digest keeps at most 150 characters of arguments and 300 of the result per step, on one line, for the newest 15 steps (it says how many earlier ones are left out).
- Arguments and results go through the guardrails' secret masking (private keys, cloud and API keys, tokens, bearer headers, `password: value` pairs, credentials in URLs). Personal data is not masked here; the guardrails policy (`guardrails.pii`) scans every assistant message sent to a model, digest included.
- For a model that is not local the digest lists tool names only (and "failed"): arguments and results of tools (mail, memory, internal documents) must not reach a hosted model that never saw them.
- Switch it off with `chat.context.digest`.

## Loop robustness

- **Retries.** A model call that fails before any event arrived (nothing shown, no side effect) is repeated up to `chat.model_retries` (2) times, after 1 s and 2 s, when the error is 429, 5xx, a connection error or the idle timeout. A failure after the first event is never retried: the text is already shown. Each try is a trace step; a repeated one has `will_retry` and the next has `attempt`.
- **Round cap.** At `chat.max_tool_iterations` (30; `cowork.max_tool_iterations`, 100, for tasks) the model gets one more call without tools and with a line asking for the final report, so a task ends with a report instead of a status line. That step has stop reason `round_cap` and `final_report` in the trace. If that call fails the run still ends normally.
- **Repeat guard.** The same tool with the same arguments `chat.repeat_limit` (3) times in a row is not run the third time; the result says so and asks for another approach. `0` turns it off.
- **Task web budget.** A task has `cowork.web.max_searches` and `cowork.web.max_fetches` (15 each) per run; a chat reply keeps `web.search.max_calls_per_turn` and `web.fetch.max_calls_per_turn` (3).
- **Parallel calls.** The calls of one round run together (at most 4 at once) only when there are several and every one belongs to the web, knowledge or files built-in sets; results keep the call order. Any other tool in the round keeps the whole round serial. `chat.parallel_tools` switches it off.

## Settings

| Key | Default | Meaning |
|---|---|---|
| `chat.context.enable` | `true` | Size tracking, clearing and mid-run summaries (spilling and the other items below have their own switch). |
| `chat.context.window_local` | `16000` | Window assumed for a local model without `context_window` or `num_ctx`. |
| `chat.context.clear_at` / `clear_to` | `0.7` / `0.5` | Start and stop of a clearing batch, as a share of the window. |
| `chat.context.keep_rounds` | `4` | Newest rounds whose results are never cleared. |
| `chat.context.compact` | `true` | Summarise the run when clearing is not enough. |
| `tools.spill_chars` | `0` | Spill results longer than this (0 = automatic, -1 = never). |
| `chat.context.digest` | `true` | Digest of earlier steps on earlier assistant turns. |
| `chat.compaction.enable` | `true` | Between-turn compaction (was `false`). |
| `chat.model_retries` | `2` | Extra tries of a failed model call. |
| `chat.repeat_limit` | `3` | Identical calls in a row before one is refused (0 = off). |
| `chat.parallel_tools` | `true` | Run read-only calls of a round together. |
| `cowork.web.max_searches` / `max_fetches` | `15` / `15` | Web budget of one task run. |

All are in Admin > Settings > Chat (and Web for the task budget). Model parameters: `context_window` (tokens), `num_ctx`, `compact_token_threshold`.

## What the trace shows

Steps of kind `compaction` (name in the Admin > Traces list): `clear` (results cleared, tokens before and after), `summarise_run` (messages summarised, tokens before and after; error `compaction_failed` when it fell back to clearing), `spill` (tool, characters, file id), `overflow_suspected` (reported prompt tokens, window). Tool steps carry `spilled` and `repeat_refused`; model steps carry `attempt`, `will_retry` and, for the closing call, `final_report` with finish `round_cap`. Window and its source (`context_window`, `num_ctx`, `setting`) are on the `clear` and `summarise_run` steps. No text is stored.

Questions to ask in SQL: how often a run needed clearing (`name = 'clear'`), the largest prompt per round (`prompt_tokens` of model steps) before and after switching `chat.context.enable`, how many model calls were retried (`detail->>'will_retry'`), how many tasks ended at the cap (`finish = 'round_cap'`).
