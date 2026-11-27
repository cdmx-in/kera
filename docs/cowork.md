# Cowork tasks

A Cowork task is a chat in task mode: you give the assistant a goal, it plans and works with the tools, files and knowledge you pick, asks when it is blocked, and leaves a report and files behind. A task survives a restart of the server, and a question it asks waits up to 12 hours without holding anything in memory ([durable runs](durable-runs.md)). Everyone with `features.cowork` (on by default) can start tasks. A task can also be saved on a timetable (scheduled), which needs `features.cowork_schedule`: on for admins, off for other users until you grant it.

## The New task page

**Cowork > New task** is a five-step wizard. The step is in the address (`/cowork/new?step=tools`), so Back, a reload and a link all land on it, and the draft is kept for the tab: a reload, or the trip out to connect an app and back, finds the goal, files and choices again. A step opens only when the steps before it are complete. People who may not schedule see four steps, without Schedule.

| Step | What it holds |
|---|---|
| Goal | The instruction in a large box (files can be attached; `/command` inserts a saved prompt; dictation works where the browser offers it), a few example goals to start from, and an optional name. Without a name a task uses the start of the goal. |
| Assistant | One card per assistant you may pick, marked **On server** (data stays on this server) or **Hosted**. The assistant's own switches (for example web search) and the knowledge collections you can read, with a search box. |
| Apps and tools | Every tool server you can see, grouped as **Your accounts** (linked with your own sign-in) and **Shared tools**, with a search box and the number switched on. Each app says why it cannot be used: **Connect needed** (with a Connect link that returns to this step), **Unavailable**, or **Local assistants only** (an app marked local-only joins only runs of an on-server assistant). **Check connections** asks every switched-on server now and shows OK or what needs attention. |
| Schedule | **Run once, now**, or every hour, every day, weekdays, every week, with the time zone. The page says the timetable in words and shows the next three runs; the server works them out, so time zones and daylight saving follow the same rules as the scheduler. Opening a saved task (`?schedule=<id>`) has no "once" choice. |
| Review | Every choice with an Edit link, then **Test it first**, then **Start task** (once), **Save schedule** or **Save changes**. |

`?when=daily` (the link on the Scheduled tab) opens the wizard with that schedule chosen; `?q=` prefills the goal, and nothing ever starts without a click.

An app that is switched on but cannot join the run (not connected, failing, local-only with a hosted assistant) is left out of the task, and the page says so. Choosing another assistant brings that assistant's own switches and apps.

## Scheduled tasks

A scheduled task is one saved instruction. At the due time the server starts an ordinary task as the owner, with the owner's connected apps. Nobody watches it: a question is refused at once, a step that needs a confirmation is skipped and reported, and a run fails and says why when a requested app is not connected. Each run keeps notes for the next one (the assistant saves them itself, and they are cleared when the assistant changes). Runs appear under **Tasks**; the **Scheduled** tab lists the timetables.

| Setting | Default | Controls |
|---|---|---|
| `cowork.schedule.max_per_user` | `5` | Scheduled tasks one person may keep. |
| `cowork.schedule.max_concurrent` | `2` | Scheduled runs and test runs going at once (`0` pauses them all). |
| `cowork.schedule.max_minutes` | `120` | Time limit of one unattended run. |

## Test runs

On the Review step, **Run a test** runs the task once, now, with exactly the settings in the wizard, and shows the result in place. It does not create a schedule, and it does not read or change any saved task's notes. A test is **read-only**:

- it runs the way a scheduled run goes (unattended: no questions), and the model is told it is a test and to say what it would have changed;
- only tools their server declares read-only are offered: an MCP tool with the `readOnlyHint` annotation, an OpenAPI operation that is a `GET`, and the built-in web, knowledge, file, code-sandbox and skill tools. Anything else, including a tool that declares nothing, is left out, and the answer starts with a line naming what was left out. If your tool server does not annotate its read tools, a test cannot use them;
- reading still happens for real: the test reads your connected apps, and a hosted assistant sees what it reads, exactly as in a normal run;
- the guardrails and hooks apply as in any run, and a test counts against `cowork.schedule.max_concurrent`. One test per person runs at a time;
- the run is kept as a normal task titled "Test · ...", marked **Test** in the list, and can be discarded from the wizard.

## HTTP routes

| Route | Permission | Does |
|---|---|---|
| `GET/POST /api/v1/cowork/schedules`, `PUT/DELETE /api/v1/cowork/schedules/{id}`, `POST /api/v1/cowork/schedules/{id}/run` | `features.cowork_schedule` | List, create, change, delete and run now. |
| `POST /api/v1/cowork/schedules/preview` | `features.cowork_schedule` | Body `{frequency, at_minute, weekday, timezone}`; answers `{runs: [epoch seconds, ...]}`, the next three runs. |
| `POST /api/v1/cowork/test-run` | `features.cowork` | Body as for a schedule (`prompt`, `model_id`, `tool_ids`, `features`, `files`, optional `title`); starts a read-only test and answers `{chat_id}`. |
| `GET /api/v1/cowork` | `features.cowork` | The task list; a test run has `"test": true`. |
