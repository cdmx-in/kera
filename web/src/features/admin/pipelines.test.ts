import { describe, expect, it } from 'vitest'
import {
  atBottom,
  byAttention,
  changeSummary,
  countLabel,
  duration,
  envFile,
  everyText,
  failedStep,
  isStuck,
  logTail,
  mainCount,
  runCommand,
  runSummary,
  stateOf,
  toneOf,
  triggerLabel,
  upToDate,
  validatePipeline,
  type Pipeline,
  type Step,
} from './pipelines'

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
  last_ok_at: null,
  last_run: null,
  ...o,
})

const run = (status: 'running' | 'ok' | 'warning' | 'failed') => ({ id: 1, pipeline: 'docs-sync', trigger: 'script', dry_run: false, status, client_done: false, error: '', started_at: '', active_at: '', finished_at: null, counts: {} })

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
  it('names the two triggers', () => {
    expect(triggerLabel('script')).toBe('Script')
    expect(triggerLabel('push')).toBe('Push')
    expect(triggerLabel('cron')).toBe('Cron')
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
