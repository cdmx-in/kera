# Durable runs

A reply in a saved chat or a Cowork task is a run. If Kera restarts or crashes while it is working, the run goes on from where it was; a question it asked stays open. Only Postgres is needed. Setting `runs.resume` (default on) turns this off: an interrupted reply then ends with an error that says the server restarted.

## What is durable

| Run | After a restart |
|---|---|
| A reply in a saved chat, a Cowork task, a scheduled or test run, including API calls with a `chat_id` | Goes on |
| An API call with a `chat_id` that sends the conversation in `messages` (no `user_message`), interrupted before the model's first reply was recorded | Ends: the caller's conversation is not stored before that |
| Temporary chats, API calls without a `chat_id`, requests that bring their own `tools` | Ends. Nothing is stored to go on from |

## How it works

Each live run of a saved chat has a row in the `run` table, owned by one Kera process under a lease that is renewed every 10 seconds and lasts 30. The row holds a checkpoint: the working conversation, tool results included. It is written after every model reply that asks for tools, after every tool call that must not be repeated, and at the end of every round of calls, in the same transaction as the message. The row is deleted when the run ends. Deleting the chat, the message or the user deletes it too, and stops the run (within about 10 seconds, and always before its next call that changes something).

A restarted run goes on from the last checkpoint: the system prompt, retrieved passages and anything inlet hooks changed are in it, and inlet hooks and compaction do not run again. A run that had no checkpoint yet starts its answer again from the beginning; no tool had run. A checkpoint that cannot be read, for instance after an upgrade that changed its format, ends the run as interrupted; it never starts over.

A process that stops cleanly (`SIGTERM`, a deploy) first drains: tool calls that are running get up to 20 seconds to finish and are recorded, no new call starts, a model reply being streamed is dropped and asked for again, and the runs are released so that the next process picks them up at once. Give the container that time: `stop_grace_period: 40s` (the shipped compose file sets it). Docker's default of 10 s cuts the drain short: that is safe, but calls still running are then in doubt and their runs wait for the lease to expire.

After a crash the next process takes a run over when its lease has expired, so within about 40 seconds.

## Tool calls are never repeated blindly

A call that was running when the process died may or may not have taken effect. Which calls may run again is recorded when the model asks for them. On resume:

- A tool that only reads is called again. That means tools a tool server declares read-only (an MCP `readOnlyHint`, an OpenAPI `GET`), and these built-ins: the chat file tools, the knowledge tools, web search and page fetch, the skill tools, the time and chat search.
- Any other call is **not** run again. The model is told "the server restarted while this call was running; it is unknown whether it took effect; check before repeating it". The code interpreter, memory and a scheduled task's notes are in this group, and so is any tool added to a built-in set later until it has been checked. This also applies when the process died just before the call started, because Kera cannot tell the two apart.
- Calls after that one, which had not started, run as normal. A clean stop between two calls marks them as not started, so a deploy puts nothing in doubt unless a call outlasts the drain.
- A call cut off by a stop gets no result at all, so the model is never told that it failed when it may have succeeded.

Kera trusts a server's read-only declaration. Do not mark a tool read-only (or serve it as a `GET`) if calling it twice matters. Fetching a page counts as reading, although a badly designed link can act when it is opened.

## Questions wait without holding anything

A question the assistant asks with `ask_user` in a saved chat is stored with the run. Nothing is held in memory while it waits, a restart does not withdraw it, and any instance can take the answer. The wait is 30 minutes in a chat and 12 hours in a task; after that the assistant is told the user did not answer. Confirmations a tool server asks for in the middle of a call (MCP elicitation, the `ask` callback), and questions in temporary chats, are still held in memory: after a restart the call counts as in doubt.

## What is checked again on resume

A run goes on only while it would still be allowed to start. It ends with an error when the account is disabled, pending or removed, access to the assistant was revoked, the Cowork (or scheduling) permission is gone, the conversation was built for a local model and the assistant now uses a hosted one, the chat was locked to local models, or an unattended run's time limit has passed (it is a wall-clock limit that includes the downtime). Web search, the code interpreter and memory stay on only if the user may still use them, and tool servers are joined again with the access the user has now. If the database, the model server or a scheduled run's tool server is not ready yet (all restarting together), the run waits and is tried again every 15 seconds for up to 5 minutes. A run is given up after its process died under it more than 3 times; clean restarts do not count.

## Several instances

Two Kera processes on one database are safe for runs: a run has one owner, and every write of its message and its row carries the owner and attempt number, so a process that was too slow cannot touch what the next one writes, and does not start another call that changes something. Cancel works from any instance. Running two instances is not yet a supported setup, though. What does not cross instances:

- Live token streaming. A browser attached to another instance sees the stored reply, updated at each checkpoint and each question.
- Answers to a tool server's mid-call confirmation, and tool-server callbacks (`/api/v1/runs/{run}/...`): they must reach the instance that runs the call.
- Files: instances need the same data directory.
- The sign-in rate limits and the pipeline locks are per process, two instances starting together are not guarded while they migrate the database, and the limit on concurrent scheduled runs can be passed by one when two instances are asked at the same moment.

## For API clients

If a restart or a takeover ends the HTTP response while the run goes on, `stream: false` answers `503` with code `run_detached`, the header `x-should-retry: false` (the OpenAI SDKs obey it) and `kera: {run_id, chat_id, message_id}`; `stream: true` ends with an error chunk of the same code and no `[DONE]`. **Do not send the request again**: the run goes on, and a second request would run its tools a second time. Read the chat (`GET /api/v1/chats/{id}`) until the message is no longer `streaming`.

A request that is not interactive (no `stream_format: "events"`) is never offered `ask_user`, so it cannot park on a question. A client of the native `events` dialect can: the stream then ends after the `ask_user` event without `done`; reopen it with `GET .../messages/{mid}/stream?after=<seq>`.

When the process dies without warning the connection just drops, and the run still resumes. A client that sends a `chat_id` should send its own `id` (the assistant message id) too, and after a dropped connection read the chat before it sends anything again. While the run is live, a request with the same `id` gets `409 busy`; after the run has ended, the same request writes the answer again and runs its tools again. A request without a `chat_id` is not durable: it ends with the process, as before.

## Limits

- The trace (Admin > Traces) of an attempt that crashed is lost, and so are the tokens of its model calls that no checkpoint had recorded (usage and spend undercount them). A model call that a restart interrupts is paid for twice.
- Outlet hooks can run twice if the process dies between them and the last save.
- Tool-server sessions are per process and start again after a restart. The tools on offer can differ if a server changed.
- Per-run budgets of web search and page fetch, and the 500-file limit of the upload callback, count from zero again after a restart.
- The checkpoint is overwritten whole, about 180 KB for a 65k-token conversation (computed, not measured), once or more per round.
- A checkpoint is plain text in the database, like the messages, and is deleted when the run ends. It holds what the model was sent and what tools returned. The stored request holds no credentials, but provider tool definitions that an inlet hook adds are stored as given, so a hook that puts a credential in them puts it in this table.
- An upgrade to a version with a different checkpoint format ends the runs that were live, as interrupted.
- Running an older and a newer version side by side on one database is not supported: an older one fails every reply it finds being written when it starts.

## Settings

| Setting | Default | Controls |
|---|---|---|
| `runs.resume` | `true` | Whether a restart resumes the runs of saved chats. Off: they end with an error. It is read when a run starts and again when one is claimed, so turning it off also ends runs that are waiting for an answer when they are next picked up. |

The lease (30 s), the heartbeat (10 s), the drain (20 s), the retry window (5 min) and the 3 recoveries are not settings.
