import { describe, expect, it } from 'vitest'
import {
  applyCells,
  atBottom,
  atMinuteOf,
  autoCheckIn,
  byAttention,
  cancellable,
  cellLines,
  cellStep,
  changeSummary,
  changedFiles,
  collapseDiff,
  countLabel,
  diffLines,
  draftChanged,
  draftOf,
  duration,
  dockerPush,
  envFile,
  everyText,
  failedStep,
  fileNameError,
  headerTitle,
  hhmm,
  isNotebookRun,
  isStuck,
  joinCells,
  laterStamp,
  lineSeverities,
  locateLine,
  logTail,
  mainCount,
  parseTime,
  problemsByCell,
  queuedText,
  runCommand,
  runnerEnvFile,
  runAllNext,
  runStatus,
  runSummary,
  scheduleBody,
  scheduleChanged,
  scheduleFormOf,
  scheduleSummary,
  scriptLabel,
  sessionLine,
  splitCells,
  stateOf,
  stepNames,
  statusLabel,
  toneOf,
  triggerLabel,
  upToDate,
  validatePipeline,
  validateRunner,
  validateSchedule,
  validateScript,
  validZone,
  type CellRun,
  type CellStatus,
  type Pipeline,
  type Problem,
  type Script,
  type SessionCell,
  type Step,
 countChips } from './pipelines'

const pipe = (o: Partial<Pipeline> = {}): Pipeline => ({
  id: 'p1',
  name: 'docs-sync',
  title: '',
  owner: { id: 'u1', name: 'Ada', email: 'ada@example.com' },
  targets: [{ id: 'k1', name: 'Product docs', missing: false }],
  every_minutes: 1440,
  token_prefix: 'kp-abcdefg',
  adopt: false,
  allow_mass_delete: false,
  enabled: true,
  managed: false,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-01T00:00:00Z',
  late: false,
  running: false,
  run_mode: 'external',
  schedule: { scheduled: false, frequency: 'daily', at_minute: 0, weekday: 1, timezone: 'UTC', next_run_at: null },
  last_ok_at: null,
  last_run: null,
  ...o,
})

const run = (status: 'running' | 'ok' | 'warning' | 'failed') => ({ id: 1, pipeline: 'docs-sync', trigger: 'script', dry_run: false, status, client_done: false, error: '', queued: false, cancel_requested: false, started_at: '', active_at: '', finished_at: null, counts: {} })

const step = (o: Partial<Step> = {}): Step => ({ seq: 1, kera: false, key: 'fetch', name: 'fetch', status: 'ok', started_at: null, finished_at: null, error: '', counts: {}, log_dropped: false, ...o })

describe('stateOf', () => {
  it('puts off first, then running, then a failed last run, then late, then the last run', () => {
    expect(stateOf(pipe({ enabled: false, running: true, late: true, last_run: run('failed') }))).toBe('off')
    expect(stateOf(pipe({ running: true, late: true, last_run: run('running') }))).toBe('running')
    expect(stateOf(pipe({ late: true, last_run: run('warning') }))).toBe('late')
    expect(stateOf(pipe({ last_run: run('failed') }))).toBe('failed')
    expect(stateOf(pipe({ last_run: run('warning') }))).toBe('warning')
    expect(stateOf(pipe({ last_run: run('ok') }))).toBe('ok')
  })
  it('keeps a failing pipeline red when it is late too', () => {
    expect(stateOf(pipe({ late: true, last_run: run('failed') }))).toBe('failed')
    expect(stateOf(pipe({ late: true, last_run: run('ok') }))).toBe('late')
  })
  it('says never run only without a run and without being late', () => {
    expect(stateOf(pipe())).toBe('never')
    expect(stateOf(pipe({ late: true }))).toBe('late')
  })
})

describe('toneOf', () => {
  it('maps every state and step status to a Badge tone', () => {
    expect(toneOf('ok')).toBe('green')
    expect(toneOf('running')).toBe('accent')
    expect(toneOf('warning')).toBe('amber')
    expect(toneOf('late')).toBe('amber')
    expect(toneOf('failed')).toBe('red')
    for (const s of ['pending', 'skipped', 'never', 'off'] as const) expect(toneOf(s)).toBe('grey')
  })
})

describe('byAttention', () => {
  it('lists failed and late first, off last, ties by name', () => {
    const list = [
      pipe({ name: 'z-ok', last_run: run('ok') }),
      pipe({ name: 'b-off', enabled: false }),
      pipe({ name: 'a-ok', last_run: run('ok') }),
      pipe({ name: 'c-late', late: true }),
      pipe({ name: 'd-failed', last_run: run('failed') }),
      pipe({ name: 'e-never' }),
      pipe({ name: 'f-late-failed', late: true, last_run: run('failed') }),
    ]
    expect(list.sort(byAttention).map((p) => p.name)).toEqual(['d-failed', 'f-late-failed', 'c-late', 'e-never', 'a-ok', 'z-ok', 'b-off'])
  })
})

describe('duration', () => {
  const t = (s: string) => `2026-03-10T06:${s}Z`
  it('reads in seconds, minutes and hours', () => {
    expect(duration(t('00:00'), t('00:12'))).toBe('12 s')
    expect(duration(t('00:00'), t('03:04'))).toBe('3 min 4 s')
    expect(duration(t('00:00'), t('03:00'))).toBe('3 min')
    expect(duration('2026-03-10T06:00:00Z', '2026-03-10T07:05:30Z')).toBe('1 h 5 min')
    expect(duration('2026-03-10T06:00:00Z', '2026-03-10T08:00:00Z')).toBe('2 h')
  })
  it('runs an open end to now, and is empty without a start', () => {
    expect(duration(t('00:00'), null, new Date(t('00:42')).getTime())).toBe('42 s')
    expect(duration(null, null)).toBe('')
    expect(duration(t('00:10'), t('00:00'))).toBe('0 s')
  })
})

describe('everyText', () => {
  it('uses the biggest unit that divides evenly', () => {
    expect(everyText(1440)).toBe('day')
    expect(everyText(10080)).toBe('7 days')
    expect(everyText(60)).toBe('hour')
    expect(everyText(360)).toBe('6 hours')
    expect(everyText(90)).toBe('90 min')
    expect(everyText(15)).toBe('15 min')
  })
})

describe('changeSummary and upToDate', () => {
  it('lists what moved', () => {
    expect(changeSummary({ listed: 25, unchanged: 20, uploaded: 4, removed: 1 })).toBe('4 changed, 1 removed')
    expect(changeSummary({ adopted: 3, failed: 2, refused: 6 })).toBe('3 adopted, 2 failed, 6 not removed')
  })
  it('says No changes, and counts the files that were already current', () => {
    expect(changeSummary({ listed: 25, unchanged: 25 })).toBe('No changes')
    expect(changeSummary(undefined)).toBe('No changes')
    expect(upToDate({ listed: 25, unchanged: 25 })).toBe(25)
    expect(upToDate({ listed: 25, unchanged: 24, uploaded: 1 })).toBe(0)
    expect(upToDate({})).toBe(0)
  })
})

describe('mainCount', () => {
  it('shows a collection its most telling number', () => {
    expect(mainCount({ kera: true, counts: { listed: 25, unchanged: 22, uploaded: 3 } })).toBe('3 changed')
    expect(mainCount({ kera: true, counts: { listed: 25, unchanged: 22, uploaded: 3, failed: 1 } })).toBe('1 failed')
    expect(mainCount({ kera: true, counts: { listed: 25, unchanged: 25 } })).toBe('25 unchanged')
    expect(mainCount({ kera: true, counts: { uploaded: 2, waiting: 2 } })).toBe('2 waiting')
    expect(mainCount({ kera: true, counts: { listed: 5, unchanged: 2, refused: 3 } })).toBe('3 not removed')
    expect(mainCount({ kera: true, counts: { refused: 3, failed: 1 } })).toBe('1 failed')
    expect(mainCount({ kera: true, counts: {} })).toBe('')
  })
  it('shows a script step its first count', () => {
    expect(mainCount({ kera: false, counts: { files: 25 } })).toBe('25 files')
    expect(mainCount({ kera: false, counts: {} })).toBe('')
  })
})

describe('failedStep', () => {
  it('blames a failed script step before a failed collection', () => {
    const steps = [step({ seq: 1, name: 'fetch' }), step({ seq: 1000, kera: true, name: 'Docs', status: 'failed' }), step({ seq: 2, name: 'convert', status: 'failed' })]
    expect(failedStep(steps)?.name).toBe('convert')
    expect(failedStep(steps.slice(0, 2))?.name).toBe('Docs')
    expect(failedStep([step()])).toBeUndefined()
    expect(failedStep(undefined)).toBeUndefined()
  })
})

describe('logTail', () => {
  it('keeps the last ten non-empty lines', () => {
    const log = Array.from({ length: 15 }, (_, i) => `06:00:${String(i).padStart(2, '0')} line ${i}`).join('\n') + '\n\n'
    const tail = logTail(log).split('\n')
    expect(tail).toHaveLength(10)
    expect(tail[0]).toContain('line 5')
    expect(tail[9]).toContain('line 14')
    expect(logTail(undefined)).toBe('')
  })
})

describe('atBottom', () => {
  it('follows only while the reader is at the end', () => {
    expect(atBottom({ scrollHeight: 1000, scrollTop: 600, clientHeight: 400 })).toBe(true)
    expect(atBottom({ scrollHeight: 1000, scrollTop: 590, clientHeight: 400 })).toBe(true)
    expect(atBottom({ scrollHeight: 1000, scrollTop: 300, clientHeight: 400 })).toBe(false)
  })
})

describe('isStuck', () => {
  const at = new Date('2026-03-10T06:00:00Z').getTime()
  it('flags an open run that has been quiet for five minutes', () => {
    expect(isStuck({ status: 'running', client_done: false, active_at: '2026-03-10T05:56:00Z' }, at)).toBe(false)
    expect(isStuck({ status: 'running', client_done: false, active_at: '2026-03-10T05:55:00Z' }, at)).toBe(true)
    expect(isStuck({ status: 'failed', client_done: false, active_at: '2026-03-10T05:00:00Z' }, at)).toBe(false)
  })
  it('leaves a run alone once its script is done: Kera is only indexing', () => {
    expect(isStuck({ status: 'running', client_done: true, active_at: '2026-03-10T05:00:00Z' }, at)).toBe(false)
  })
})

describe('runSummary', () => {
  it('says what moved, or the error of a failed run', () => {
    expect(runSummary({ status: 'ok', error: '', counts: { uploaded: 4, removed: 1 } })).toBe('4 changed, 1 removed')
    expect(runSummary({ status: 'ok', error: '', counts: {} })).toBe('No changes')
    expect(runSummary({ status: 'running', error: '', counts: {} })).toBe('In progress')
    expect(runSummary({ status: 'failed', error: 'convert exited with code 3', counts: {} })).toBe('convert exited with code 3')
    expect(runSummary({ status: 'failed', error: '', counts: { failed: 2 } })).toBe('2 failed')
    expect(runSummary({ status: 'warning', error: 'Prune refused', counts: { refused: 3 } })).toBe('3 not removed')
  })
})

describe('triggerLabel', () => {
  it('names the four triggers a run can have', () => {
    expect(triggerLabel('schedule')).toBe('Schedule')
    expect(triggerLabel('manual')).toBe('Manual')
    expect(triggerLabel('test')).toBe('Notebook')
    expect(isNotebookRun({ trigger: 'test' })).toBe(true)
    expect(isNotebookRun({ trigger: 'manual' })).toBe(false)
    expect(triggerLabel('script')).toBe('Script')
    expect(triggerLabel('push')).toBe('Push')
    expect(triggerLabel('cron')).toBe('Cron')
  })
})

describe('dockerPush', () => {
  const p = { targets: [{ id: 'k1', name: 'Product docs', missing: false }] }
  it('uses the address as it is for a real host name', () => {
    const c = dockerPush(p, 'https://kera.example.com')
    expect(c).toContain('--env-file kera.env -v "$PWD/docs:/docs:ro"')
    expect(c).not.toContain('host.docker.internal')
  })
  it('points a container at the host when Kera is opened on localhost', () => {
    for (const origin of ['http://localhost:8080', 'http://127.0.0.1:8080']) {
      const c = dockerPush(p, origin)
      expect(c).toContain('--add-host host.docker.internal:host-gateway -e KERA_URL=http://host.docker.internal:8080')
      expect(c.indexOf('--env-file')).toBeLessThan(c.indexOf('-e KERA_URL')) // the later -e wins over the env file
    }
  })
})

describe('runCommand and envFile', () => {
  it('builds the push line for the first live collection and quotes it', () => {
    const p = { name: 'docs-sync', targets: [{ id: 'k0', name: 'Old', missing: true }, { id: 'k1', name: 'Product docs', missing: false }] }
    expect(runCommand(p, 'https://kera.example.com')).toBe(
      "KERA_URL=https://kera.example.com KERA_PIPELINE=docs-sync KERA_PIPELINE_TOKEN=<token> \\\nkera pipeline push --collection 'Product docs' --dir ./docs",
    )
    expect(runCommand({ name: 'a', targets: [{ id: 'k', name: "It's docs", missing: false }] }, 'http://x', 'kp-1')).toContain(`--collection 'It'\\''s docs'`)
    expect(runCommand({ name: 'a', targets: [{ id: 'k', name: 'docs', missing: false }] }, 'http://x')).toContain('--collection docs ')
    expect(runCommand({ name: 'a', targets: [] }, 'http://x')).not.toContain('--collection')
  })
  it('writes an env file', () => {
    expect(envFile('docs-sync', 'https://kera.example.com', 'kp-secret')).toBe('KERA_URL=https://kera.example.com\nKERA_PIPELINE=docs-sync\nKERA_PIPELINE_TOKEN=kp-secret')
  })
})

describe('validatePipeline', () => {
  const ok = { name: 'docs-sync', every: '1440', targets: ['k1'], fresh: [] as string[] }
  it('accepts a good form, and lets an edit keep its name', () => {
    expect(validatePipeline(ok, true)).toEqual({})
    expect(validatePipeline({ ...ok, name: 'Not A Slug' }, false)).toEqual({})
  })
  it('checks the name, the interval and the collections', () => {
    expect(validatePipeline({ ...ok, name: 'Docs Sync' }, true).name).toBeTruthy()
    expect(validatePipeline({ ...ok, name: '-docs' }, true).name).toBeTruthy()
    expect(validatePipeline({ ...ok, name: 'a'.repeat(41) }, true).name).toBeTruthy()
    expect(validatePipeline({ ...ok, every: '' }, true).every_minutes).toBeTruthy()
    expect(validatePipeline({ ...ok, every: '1.5' }, true).every_minutes).toBeTruthy()
    expect(validatePipeline({ ...ok, every: '-1' }, true).every_minutes).toBeTruthy()
    expect(validatePipeline({ ...ok, every: '525601' }, true).every_minutes).toBeTruthy()
    expect(validatePipeline({ ...ok, every: '0' }, true)).toEqual({})
    expect(validatePipeline({ ...ok, targets: [] }, true).targets).toBeTruthy()
    expect(validatePipeline({ ...ok, targets: [], fresh: ['Product docs'] }, true)).toEqual({})
  })
})

describe('countLabel', () => {
  it('words the counts', () => {
    expect(countLabel('listed')).toBe('Listed')
    expect(countLabel('refused')).toBe('Removals refused')
  })
})

describe('countChips', () => {
  it('lists a collection\'s non-zero counts, worst first, at most three', () => {
    const c = countChips({ kera: true, counts: { listed: 30, unchanged: 22, adopted: 4, uploaded: 3, removed: 1, failed: 1 } })
    expect(c.map((x) => x.text)).toEqual(['1 failed', '3 changed', '1 removed'])
    expect(c[0].tone).toBe('red')
  })
  it('shows nothing for a collection with no counts, and a script step\'s own counts as they come', () => {
    expect(countChips({ kera: true, counts: {} })).toEqual([])
    expect(countChips({ kera: false, counts: { files: 25 } }).map((x) => x.text)).toEqual(['25 files'])
  })
})

const q = (o: Record<string, unknown> = {}) => ({ ...run('running'), queued: true, ...o })

describe('a queued run', () => {
  it('makes the pipeline Queued, not Running, until a runner claims it', () => {
    expect(stateOf(pipe({ running: true, last_run: q() }))).toBe('queued')
    expect(stateOf(pipe({ running: true, last_run: { ...q(), queued: false } }))).toBe('running')
    expect(statusLabel.queued).toBe('Queued')
    expect(toneOf('queued')).toBe('grey')
  })
  it('keeps off and a failed last run ahead of it, and sorts it just after Running', () => {
    expect(stateOf(pipe({ enabled: false, running: true, last_run: q() }))).toBe('off')
    const list = [pipe({ name: 'c', last_run: run('ok') }), pipe({ name: 'b', running: true, last_run: q() }), pipe({ name: 'a', running: true, last_run: { ...q(), queued: false } })]
    expect([...list].sort(byAttention).map((p) => p.name)).toEqual(['a', 'b', 'c'])
  })
  it('reads Queued: waiting for a runner, and Stopping once a stop is asked for', () => {
    expect(runStatus(q())).toBe('queued')
    expect(runStatus({ status: 'running', queued: false })).toBe('running')
    expect(runStatus({ status: 'failed', queued: true })).toBe('failed')
    expect(queuedText).toBe('Queued: waiting for a runner')
    expect(runSummary({ ...q(), error: '', counts: {} })).toBe('Queued: waiting for a runner')
    expect(runSummary({ ...run('running'), cancel_requested: true, error: '', counts: {} })).toBe('Stopping…')
    expect(runSummary({ ...run('running'), error: '', counts: {} })).toBe('In progress')
  })
  it('is never offered Close run: it reported nothing because no runner has it yet', () => {
    const quiet = { status: 'running' as const, active_at: '2026-03-10T06:00:00Z', client_done: false }
    const later = new Date('2026-03-10T07:00:00Z').getTime()
    expect(isStuck(quiet, later)).toBe(true)
    expect(isStuck({ ...quiet, queued: true }, later)).toBe(false)
  })
  it('offers Cancel for a Kera-run run that is open, not for one a script reports itself', () => {
    expect(cancellable(q())).toBe(true)
    expect(cancellable({ status: 'running', queued: false, script_version: 3 })).toBe(true)
    expect(cancellable({ status: 'running', queued: false })).toBe(false)
    expect(cancellable({ status: 'ok', queued: false, script_version: 3 })).toBe(false)
  })
  it('names the script version a run used', () => {
    expect(scriptLabel(3)).toBe('script v3')
    expect(scriptLabel(undefined)).toBe('')
  })
})

describe('timetable', () => {
  const sched = (o = {}) => ({ scheduled: true, frequency: 'daily' as const, at_minute: 570, weekday: 1, timezone: 'Asia/Kolkata', next_run_at: null, ...o })
  it('turns minutes into HH:MM and back, refusing what is not a time', () => {
    expect(hhmm(570)).toBe('09:30')
    expect(hhmm(0)).toBe('00:00')
    expect(parseTime('09:30')).toBe(570)
    expect(parseTime('9:05')).toBe(545)
    expect(parseTime('24:00')).toBeNull()
    expect(parseTime('12:60')).toBeNull()
    expect(parseTime('')).toBeNull()
  })
  it('says the timetable in words', () => {
    expect(scheduleSummary(sched())).toBe('Every day at 09:30 (Asia/Kolkata)')
    expect(scheduleSummary(sched({ frequency: 'weekdays' }))).toBe('Every weekday at 09:30 (Asia/Kolkata)')
    expect(scheduleSummary(sched({ frequency: 'weekly', weekday: 0, at_minute: 375, timezone: 'UTC' }))).toBe('Every Sunday at 06:15 (UTC)')
    expect(scheduleSummary(sched({ frequency: 'hourly', at_minute: 15 }))).toBe('Every hour at minute 15')
  })
  it('starts a pipeline that something else runs on the browser time zone, and a Kera-run one on its own', () => {
    const external = scheduleFormOf(pipe({ schedule: sched({ timezone: 'UTC' }) }), 'Europe/Berlin')
    expect(external).toMatchObject({ mode: 'external', scheduled: false, timezone: 'Europe/Berlin' })
    expect(scheduleFormOf(null, 'Europe/Berlin').mode).toBe('external')
    const kera = scheduleFormOf(pipe({ run_mode: 'kera', schedule: sched() }), 'Europe/Berlin')
    expect(kera).toMatchObject({ mode: 'kera', scheduled: true, frequency: 'daily', time: '09:30', timezone: 'Asia/Kolkata', weekday: 1 })
  })
  it('checks the time, the minute of an hourly one and the time zone, under the server field names', () => {
    const f = scheduleFormOf(pipe({ run_mode: 'kera', schedule: sched() }), 'UTC')
    expect(validateSchedule(f)).toEqual({})
    expect(validateSchedule({ ...f, time: '' }).at_minute).toBe('Choose a time of day')
    expect(validateSchedule({ ...f, frequency: 'hourly', minute: '75' }).at_minute).toBe('A minute from 0 to 59')
    expect(validateSchedule({ ...f, frequency: 'hourly', minute: '15' })).toEqual({})
    expect(validateSchedule({ ...f, timezone: 'Mars/Olympus' }).timezone).toBeTruthy()
    expect(validateSchedule({ ...f, timezone: '' }).timezone).toBeTruthy()
    expect(validateSchedule({ ...f, timezone: 'Local' }).timezone).toBeTruthy()
    expect(validateSchedule({ ...f, mode: 'external', time: '', timezone: '' })).toEqual({})
    expect(validZone('UTC')).toBe(true)
    expect(validZone('Asia/Kolkata')).toBe(true)
  })
  it('sends the minute of the hour for hourly and minutes after midnight for the rest', () => {
    const f = scheduleFormOf(pipe({ run_mode: 'kera', schedule: sched() }), 'UTC')
    expect(atMinuteOf(f)).toBe(570)
    expect(atMinuteOf({ ...f, frequency: 'hourly', minute: '15' })).toBe(15)
    expect(scheduleBody(f)).toEqual({ run_mode: 'kera', scheduled: true, frequency: 'daily', at_minute: 570, weekday: 1, timezone: 'Asia/Kolkata' })
    expect(scheduleBody({ ...f, mode: 'external' }).scheduled).toBe(false)
    expect(scheduleBody({ ...f, timezone: ' UTC ' }).timezone).toBe('UTC')
  })
  it('sends the timetable only when it changed, because every save recomputes the next run', () => {
    const p = pipe({ run_mode: 'kera', schedule: sched() })
    const f = scheduleFormOf(p, 'UTC')
    expect(scheduleChanged(p, f)).toBe(false)
    expect(scheduleChanged(p, { ...f, time: '10:00' })).toBe(true)
    expect(scheduleChanged(p, { ...f, scheduled: false })).toBe(true)
    expect(scheduleChanged(p, { ...f, mode: 'external' })).toBe(true) // back to started from outside
    expect(scheduleChanged(pipe(), scheduleFormOf(pipe(), 'UTC'))).toBe(false)
    expect(scheduleChanged(pipe(), { ...scheduleFormOf(pipe(), 'UTC'), mode: 'kera' })).toBe(true)
    expect(scheduleChanged(null, { ...scheduleFormOf(null, 'UTC'), mode: 'kera' })).toBe(true)
  })
  it('does not ask for Expected every when Kera runs the pipeline', () => {
    const f = { name: 'docs', every: '', targets: ['k1'], fresh: [] }
    expect(validatePipeline(f, true).every_minutes).toBeTruthy()
    expect(validatePipeline({ ...f, mode: 'kera' }, true).every_minutes).toBeUndefined()
  })
})

describe('script', () => {
  const saved = (o: Partial<Script> = {}): Script => ({ version: 2, files: { 'run.sh': 'echo hi\n', 'lib/a.py': 'x = 1\n' }, entry: 'run.sh', args: '--full', note: '', created_by: 'Ada', created_at: '2026-03-10T06:00:00Z', ...o })
  it('has a draft that equals what is saved until something is typed (the note does not count)', () => {
    const s = saved()
    const d = draftOf(s)
    expect(draftChanged(s, d)).toBe(false)
    expect(draftChanged(s, { ...d, note: 'tidy' })).toBe(false)
    expect(draftChanged(s, { ...d, args: ' --full ' })).toBe(false)
    expect(draftChanged(s, { ...d, args: '' })).toBe(true)
    expect(draftChanged(s, { ...d, entry: 'lib/a.py' })).toBe(true)
    expect(draftChanged(s, { ...d, files: { ...d.files, 'run.sh': 'echo bye\n' } })).toBe(true)
    expect(draftChanged(s, { ...d, files: { 'run.sh': d.files['run.sh'] } })).toBe(true) // a file was deleted
    expect(draftChanged(null, draftOf(null))).toBe(false)
    expect(draftChanged(null, { ...draftOf(null), files: { 'run.sh': '' } })).toBe(true)
  })
  it('lists the files that are new or different', () => {
    const s = saved()
    expect(changedFiles(s, { ...s.files, 'run.sh': 'echo bye\n', 'new.sh': '' }).sort()).toEqual(['new.sh', 'run.sh'])
    expect(changedFiles(null, { 'a.sh': '' })).toEqual(['a.sh'])
  })
  it('accepts plain relative paths for a new file and says why not otherwise', () => {
    expect(fileNameError('scripts/fetch.py', [])).toBe('')
    expect(fileNameError('my notes.txt', [])).toBe('')
    expect(fileNameError('', [])).toBeTruthy()
    for (const bad of ['/etc/passwd', '../x.sh', 'a/../b.sh', 'a//b.sh', 'dir/', './a.sh', '-lead.sh']) expect(fileNameError(bad, []), bad).toBeTruthy()
    expect(fileNameError('run.sh', ['run.sh'])).toBe('A file with this name exists')
  })
  it('needs a file, an entry that is one of them and ends in .sh, .bash or .py, and a one-line argument list', () => {
    const d = draftOf(saved())
    expect(validateScript(d)).toEqual({})
    expect(validateScript({ ...d, files: {}, entry: '' })).toMatchObject({ files: 'Add at least one file', entry: 'Choose the file that is started' })
    expect(validateScript({ ...d, entry: 'gone.sh' }).entry).toBe('Choose the file that is started')
    expect(validateScript({ ...d, files: { 'notes.txt': '' }, entry: 'notes.txt' }).entry).toBe('The entry file must end in .sh, .bash or .py')
    expect(validateScript({ ...d, args: 'a\nb' }).args).toBeTruthy()
    expect(validateScript({ ...d, args: 'x'.repeat(501) }).args).toBeTruthy()
  })
})

describe('diffLines', () => {
  const show = (a: string, b: string) => diffLines(a, b).map((l) => ({ same: ' ', add: '+', del: '-' })[l.kind] + l.text)
  it('is all unchanged for equal text, and empty for two empty texts', () => {
    expect(show('a\nb', 'a\nb')).toEqual([' a', ' b'])
    expect(show('', '')).toEqual([])
  })
  it('marks added and removed lines in order', () => {
    expect(show('a\nb\nc', 'a\nc')).toEqual([' a', '-b', ' c'])
    expect(show('a\nc', 'a\nb\nc')).toEqual([' a', '+b', ' c'])
    expect(show('a\nb\nc', 'a\nX\nc')).toEqual([' a', '-b', '+X', ' c'])
  })
  it('handles an empty side as all added or all removed', () => {
    expect(show('', 'a\nb')).toEqual(['+a', '+b'])
    expect(show('a\nb', '')).toEqual(['-a', '-b'])
  })
  it('finds the common lines between two edits instead of replacing everything', () => {
    expect(show('1\n2\n3\n4\n5', '1\nX\n3\nY\n5')).toEqual([' 1', '-2', '+X', ' 3', '-4', '+Y', ' 5'])
    expect(show('a\nb\nc\nd', 'b\nc\nd\ne')).toEqual(['-a', ' b', ' c', ' d', '+e'])
  })
  it('puts a changed line in each direction as the inverse of the other', () => {
    const a = 'one\ntwo\nthree\nfour'
    const b = 'one\nthree\nfour\nfive'
    const fwd = diffLines(a, b)
    const back = diffLines(b, a)
    expect(fwd.filter((l) => l.kind === 'add').map((l) => l.text)).toEqual(back.filter((l) => l.kind === 'del').map((l) => l.text))
    expect(fwd.filter((l) => l.kind === 'del').map((l) => l.text)).toEqual(back.filter((l) => l.kind === 'add').map((l) => l.text))
  })
  it('does not build a huge table for two big files: the changed middle is shown as removed, then added', () => {
    const a = Array.from({ length: 3000 }, (_, i) => `a${i}`).join('\n')
    const b = Array.from({ length: 3000 }, (_, i) => `b${i}`).join('\n')
    const d = diffLines(a, b)
    expect(d).toHaveLength(6000)
    expect(d[0].kind).toBe('del')
    expect(d[5999].kind).toBe('add')
  })
  it('folds long unchanged runs but keeps context around a change', () => {
    const body = Array.from({ length: 30 }, (_, i) => `line${i}`)
    const edited = [...body.slice(0, 15), 'CHANGED', ...body.slice(16)].join('\n')
    const items = collapseDiff(diffLines(body.join('\n'), edited), 2)
    const kinds = items.map((i) => i.kind)
    expect(kinds.filter((k) => k === 'skip')).toHaveLength(2)
    expect(items.filter((i) => i.kind === 'same')).toHaveLength(4) // two lines of context each side
    expect(items.find((i) => i.kind === 'skip')).toEqual({ kind: 'skip', count: 13 })
    expect(collapseDiff(diffLines('a', 'a'))).toEqual([{ kind: 'same', text: 'a' }]) // a fold of one line is kept as it is
  })
})

describe('runners', () => {
  it('writes the env file a runner container reads', () => {
    expect(runnerEnvFile('https://kera.example.com', 'kr-abc')).toBe('KERA_URL=https://kera.example.com\nKERA_RUNNER_TOKEN=kr-abc')
  })
  it('names a runner with the same slug as a pipeline', () => {
    expect(validateRunner({ name: 'build-server' })).toEqual({})
    expect(validateRunner({ name: ' build-server ' })).toEqual({})
    expect(validateRunner({ name: 'Build Server' }).name).toBeTruthy()
    expect(validateRunner({ name: '' }).name).toBeTruthy()
  })
})

describe('notebook cells', () => {
  const samples: Record<string, string> = {
    'an empty script': '',
    'no headers': 'echo hi\nls\n',
    'one line': 'echo hi',
    'a leading setup cell': 'set -e\nX=1\n#%% fetch data\necho $X\n#%% build\nmake',
    'no setup, starting with a header': '#%% first\na\n#%% second\nb\n',
    'odd spacing': '#%%a\nx\n#%%    spaced   out   \ny\n#%%\nz\n#%%\t tab\nw',
    'a header right after a header': '#%% a\n#%% b\nx\n#%% c',
    'a header on the last line': 'x\n#%% last',
    'an empty line after a header': '#%% a\n\n#%% b\n\n\nx\n',
    'blank lines before the first header': '\n\n#%% a\nx',
    'CRLF': 'set -e\r\n#%% fetch\r\necho 1\r\n\r\n#%%  build  \r\nmake\r\n',
    'a header-like line that is indented': 'x\n  #%% not a header\ny',
    'a comment that is not a header': '# %% no\n#% no\nx',
  }
  it.each(Object.entries(samples))('round-trips %s exactly', (_, text) => {
    expect(joinCells(splitCells(text))).toBe(text)
  })
  it('keeps a script without headers as the one unnamed cell', () => {
    const c = splitCells('echo hi\nls\n')
    expect(c).toHaveLength(1)
    expect(c[0]).toMatchObject({ header: null, code: 'echo hi\nls\n', bare: false })
    expect(splitCells('')).toEqual([{ title: '', header: null, code: '', bare: false }])
  })
  it('makes the text before the first header the setup cell, and none when a header comes first', () => {
    const c = splitCells('set -e\n#%% fetch data\ncurl x\n#%% build\nmake')
    expect(c.map((x) => [x.title, x.header, x.code])).toEqual([
      ['', null, 'set -e'],
      ['fetch data', '#%% fetch data', 'curl x'],
      ['build', '#%% build', 'make'],
    ])
    expect(splitCells('#%% only\nx')[0].header).toBe('#%% only')
  })
  it('reads titles like the runner: trimmed, with odd spacing and CRLF', () => {
    expect(headerTitle('#%%a')).toBe('a')
    expect(headerTitle('#%%    spaced   out   ')).toBe('spaced   out')
    expect(headerTitle('#%% name\r')).toBe('name')
    expect(headerTitle('#%%')).toBe('')
    expect(headerTitle('  #%% no')).toBeNull()
    expect(headerTitle('# %% no')).toBeNull()
    expect(splitCells('#%% a\r\nx\r\n').map((c) => c.title)).toEqual(['a'])
  })
  it('tells a cell with no lines from one with an empty line', () => {
    const c = splitCells('#%% a\n#%% b\n\n#%% c')
    expect(c.map((x) => [x.bare, x.code])).toEqual([[true, ''], [false, ''], [true, '']])
  })
  it('rewrites the header only when the name changed', () => {
    const c = splitCells('#%%   keep   me  \nx\n#%% old\ny')
    expect(joinCells(c)).toBe('#%%   keep   me  \nx\n#%% old\ny')
    c[1] = { ...c[1], title: 'new name' }
    expect(joinCells(c)).toBe('#%%   keep   me  \nx\n#%% new name\ny')
    c[1] = { ...c[1], title: '' }
    expect(joinCells(c)).toBe('#%%   keep   me  \nx\n#%%\ny')
  })
  it('never lets a headerless cell glue itself to the cell above', () => {
    const c = splitCells('setup\n#%% a\nx')
    expect(joinCells([c[1], c[0]])).toBe('#%% a\nx\n#%%\nsetup')
    expect(splitCells(joinCells([c[1], c[0]]))).toHaveLength(2)
  })
  it('names steps like the runner: one word, cell-N for an empty title, setup has none', () => {
    expect(cellStep('fetch data', 1)).toBe('fetch-data')
    expect(cellStep('  Build & test!  ', 2)).toBe('Build-test')
    expect(cellStep('', 3)).toBe('cell-3')
    expect(cellStep('***', 4)).toBe('cell-4')
    expect(cellStep('x'.repeat(80), 1)).toHaveLength(60)
    expect(stepNames(splitCells('setup\n#%% fetch data\na\n#%%\nb'))).toEqual([null, 'fetch-data', 'cell-2'])
  })
  it('numbers lines through the whole file', () => {
    const c = splitCells('set -e\n#%% a\nx\ny\n#%% b\n#%% c\nz')
    expect(cellLines(c)).toEqual([
      { header: null, first: 1, count: 1 },
      { header: 2, first: 3, count: 2 },
      { header: 5, first: 6, count: 0 },
      { header: 6, first: 7, count: 1 },
    ])
    const last = cellLines(c).at(-1)!
    expect(last.first + last.count - 1).toBe('set -e\n#%% a\nx\ny\n#%% b\n#%% c\nz'.split('\n').length)
  })
})

describe('mapping problems to cells', () => {
  const text = 'set -e\n#%% a\nx\ny\n#%% b\nz'
  const cells = splitCells(text)
  const prob = (line: number, severity: Problem['severity'] = 'error', file = 'run.sh'): Problem => ({ file, line, col: 1, severity, message: 'm', tool: 'bash -n' })
  it('finds the cell and the line within it', () => {
    expect(locateLine(cells, 1)).toEqual({ cell: 0, line: 1 })
    expect(locateLine(cells, 2)).toEqual({ cell: 1, line: 0 }) // the header line
    expect(locateLine(cells, 3)).toEqual({ cell: 1, line: 1 })
    expect(locateLine(cells, 4)).toEqual({ cell: 1, line: 2 })
    expect(locateLine(cells, 6)).toEqual({ cell: 2, line: 1 })
  })
  it('puts a line past the end in the last cell, and nothing for line 0', () => {
    expect(locateLine(cells, 99)).toEqual({ cell: 2, line: 1 })
    expect(locateLine(cells, 0)).toBeNull()
    expect(locateLine([], 1)).toBeNull()
  })
  it('counts errors and warnings per cell and marks the worst severity per line', () => {
    const r = problemsByCell(cells, [prob(1, 'warning'), prob(4), prob(4, 'warning'), prob(6, 'info'), prob(3, 'error', 'other.sh')], 'run.sh')
    expect(r[0]).toEqual({ errors: 0, warnings: 1, lines: { 1: 'warning' } })
    expect(r[1]).toEqual({ errors: 1, warnings: 1, lines: { 2: 'error' } })
    expect(r[2]).toEqual({ errors: 0, warnings: 0, lines: { 1: 'info' } })
  })
  it('marks the header line as line 0 of its cell', () => {
    expect(problemsByCell(cells, [prob(5)], 'run.sh')[2].lines).toEqual({ 0: 'error' })
  })
  it('keeps the lines of another file out of the cells, and gives them to that file', () => {
    const ps = [prob(3, 'error', 'lib/a.py'), prob(3, 'warning', 'lib/a.py'), prob(9, 'info', 'lib/a.py'), prob(1)]
    expect(lineSeverities(ps, 'lib/a.py')).toEqual({ 3: 'error', 9: 'info' })
    expect(problemsByCell(cells, ps, 'run.sh').reduce((n, c) => n + c.errors, 0)).toBe(1)
  })
})

describe('Run all sequencing', () => {
  const ids = ['a', 'b', 'c']
  const st = (o: Record<string, CellStatus | undefined>) => o
  it('starts with the first cell', () => {
    expect(runAllNext(ids, st({}), -1)).toEqual({ action: 'run', index: 0 })
    expect(runAllNext([], st({}), -1)).toEqual({ action: 'done' })
  })
  it('waits while the last cell it sent is queued or running', () => {
    expect(runAllNext(ids, st({ a: 'queued' }), 0)).toEqual({ action: 'wait' })
    expect(runAllNext(ids, st({ a: 'running' }), 0)).toEqual({ action: 'wait' })
  })
  it('sends the next cell once the last one is OK, and is done after the last', () => {
    expect(runAllNext(ids, st({ a: 'ok' }), 0)).toEqual({ action: 'run', index: 1 })
    expect(runAllNext(ids, st({ a: 'ok', b: 'ok' }), 1)).toEqual({ action: 'run', index: 2 })
    expect(runAllNext(ids, st({ a: 'ok', b: 'ok', c: 'ok' }), 2)).toEqual({ action: 'done' })
  })
  it('stops at the first failed or stopped cell and sends nothing after it', () => {
    expect(runAllNext(ids, st({ a: 'ok', b: 'failed' }), 1)).toEqual({ action: 'stop', index: 1, reason: 'failed' })
    expect(runAllNext(ids, st({ a: 'stopped' }), 0)).toEqual({ action: 'stop', index: 0, reason: 'stopped' })
  })
  it('stops when the cell it sent has no status any more (the request failed)', () => {
    expect(runAllNext(ids, st({}), 0)).toEqual({ action: 'stop', index: 0, reason: 'lost' })
  })
  it('runs up to a cell by being given only the cells up to it', () => {
    expect(runAllNext(ids.slice(0, 2), st({ a: 'ok', b: 'ok' }), 1)).toEqual({ action: 'done' })
  })
})

describe('autoCheckIn', () => {
  const base = { online: true, checking: false, editedAt: 10_000, checkedAt: 0, now: 10_400, entry: 'run.sh' }
  it('is due 1.5 s after the last edit', () => {
    expect(autoCheckIn(base)).toBe(1100)
    expect(autoCheckIn({ ...base, now: 12_000 })).toBe(0)
  })
  it('waits for a runner to be known online', () => {
    expect(autoCheckIn({ ...base, online: false })).toBeNull()
    expect(autoCheckIn({ ...base, online: undefined })).toBeNull()
  })
  it('does not start a second check, or check what was not edited since the last one', () => {
    expect(autoCheckIn({ ...base, checking: true })).toBeNull()
    expect(autoCheckIn({ ...base, checkedAt: 10_000 })).toBeNull()
    expect(autoCheckIn({ ...base, checkedAt: 10_001 })).toBeNull()
    expect(autoCheckIn({ ...base, checkedAt: 9_999 })).toBe(1100)
  })
  it('only checks a script the runner can check', () => {
    expect(autoCheckIn({ ...base, entry: '' })).toBeNull()
    expect(autoCheckIn({ ...base, entry: 'notes.txt' })).toBeNull()
    expect(autoCheckIn({ ...base, entry: 'main.py' })).toBe(1100)
  })
})

describe('session polling', () => {
  const run = (o: Partial<CellRun> = {}): CellRun => ({ server: 7, status: 'queued', output: '', exit_code: null, sent: 'echo hi', ...o })
  const cell = (o: Partial<SessionCell> = {}): SessionCell => ({ id: 7, name: 'a', status: 'running', output: 'hi\n', exit_code: null, started_at: null, finished_at: null, updated_at: '2026-03-10T06:00:00.5Z', ...o })
  it('merges a polled cell into the editor cell that sent it, keeping what was sent', () => {
    const runs = applyCells({ c1: run(), c2: run({ server: 8 }) }, [cell()])
    expect(runs.c1).toEqual({ server: 7, status: 'running', output: 'hi\n', exit_code: null, sent: 'echo hi' })
    expect(runs.c2.status).toBe('queued')
  })
  it('ignores a cell it does not know, such as a run that was replaced by a newer one', () => {
    const before = { c1: run({ server: 9 }) }
    expect(applyCells(before, [cell({ id: 7 })])).toBe(before)
  })
  it('returns the same object when nothing changed', () => {
    const before = { c1: run({ status: 'running', output: 'hi\n' }) }
    expect(applyCells(before, [cell()])).toBe(before)
  })
  it('compares timestamps whose fractions were written with different lengths', () => {
    expect(laterStamp('', '2026-03-10T06:00:00.5Z')).toBe('2026-03-10T06:00:00.5Z')
    expect(laterStamp('2026-03-10T06:00:00.5Z', '2026-03-10T06:00:00.50001Z')).toBe('2026-03-10T06:00:00.50001Z')
    expect(laterStamp('2026-03-10T06:00:00.50001Z', '2026-03-10T06:00:00.5Z')).toBe('2026-03-10T06:00:00.50001Z')
    expect(laterStamp('2026-03-10T06:00:01Z', '2026-03-10T06:00:00.9Z')).toBe('2026-03-10T06:00:01Z')
  })
  it('says where the session is, and why it ended', () => {
    expect(sessionLine(null)).toBe('No session yet')
    expect(sessionLine(null, true)).toBe('No runner is online')
    expect(sessionLine({ status: 'starting', error: '' })).toBe('Starting…')
    expect(sessionLine({ status: 'ready', error: '' })).toBe('Ready')
    expect(sessionLine({ status: 'closing', error: '' })).toBe('Closing…')
    expect(sessionLine({ status: 'closed', error: 'closed after being idle' })).toBe('Closed: closed after being idle')
  })
})
