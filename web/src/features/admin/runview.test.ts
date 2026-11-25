import { describe, expect, it } from 'vitest'
import type { Run, Step } from './pipelines'
import {
  axisLabel,
  axisSpan,
  axisTicks,
  barOf,
  baselineRun,
  clock,
  firstLine,
  healthSummary,
  healthTicks,
  keraSteps,
  median,
  opensByDefault,
  orderedSteps,
  runChip,
  runMs,
  runSentence,
  scriptSteps,
  slowerThanUsual,
  stepsDone,
  syncQuiet,
  syncSegments,
  typicalMs,
  usualSteps,
} from './runview'

const T0 = new Date('2026-03-10T06:00:00Z').getTime()
const at = (sec: number) => new Date(T0 + sec * 1000).toISOString()
const run = (o: Partial<Run> = {}): Run => ({ id: 1, pipeline: 'docs-sync', trigger: 'schedule', dry_run: false, status: 'ok', client_done: true, error: '', queued: false, cancel_requested: false, started_at: at(0), active_at: at(0), finished_at: at(100), counts: {}, ...o })
const step = (o: Partial<Step> = {}): Step => ({ seq: 1, kera: false, key: 'k', name: 'fetch', status: 'ok', started_at: at(0), finished_at: at(10), error: '', counts: {}, log_dropped: false, ...o })

describe('durations', () => {
  it('measures a run, an open one to now', () => {
    expect(runMs(run(), 0)).toBe(100_000)
    expect(runMs(run({ finished_at: null }), T0 + 30_000)).toBe(30_000)
  })
  it('takes the median of an odd and an even list, and none of an empty one', () => {
    expect(median([5, 1, 3])).toBe(3)
    expect(median([1, 2, 3, 10])).toBe(3)
    expect(median([])).toBeNull()
  })
  it('knows the usual run length from finished OK runs only, and needs three of them', () => {
    const ok = (id: number, secs: number) => run({ id, finished_at: at(secs) })
    const runs = [ok(9, 100), run({ id: 8, status: 'failed', finished_at: at(5) }), ok(7, 300), run({ id: 6, dry_run: true, finished_at: at(1) }), ok(5, 200), run({ id: 4, trigger: 'test', finished_at: at(2) })]
    expect(typicalMs(runs)).toBe(200_000)
    expect(typicalMs(runs.slice(0, 3))).toBeNull() // two good runs are not enough to go by
  })
  it('picks the newest good run other than the open one as the baseline', () => {
    const runs = [run({ id: 9, status: 'running', finished_at: null }), run({ id: 8 }), run({ id: 7 })]
    expect(baselineRun(runs, 8)?.id).toBe(7)
    expect(baselineRun(runs, undefined)?.id).toBe(8)
  })
  it('keeps what each OK step took by name, the longer of two with the same name', () => {
    expect(usualSteps([step({ name: 'a' }), step({ seq: 2, name: 'b', finished_at: at(30) }), step({ seq: 3, name: 'a', finished_at: at(20) }), step({ seq: 4, name: 'c', status: 'failed' })])).toEqual({ a: 20_000, b: 30_000 })
  })
  it('calls a step slow only when it is 1.5 times and 10 s over the usual', () => {
    expect(slowerThanUsual(40_000, 20_000)).toBe(true)
    expect(slowerThanUsual(25_000, 20_000)).toBe(false) // not 1.5 times
    expect(slowerThanUsual(6_000, 2_000)).toBe(false) // 3 times, but only 4 s more
    expect(slowerThanUsual(40_000, undefined)).toBe(false)
  })
})

describe('health strip', () => {
  const runs = [run({ id: 4, status: 'running', finished_at: null }), run({ id: 3, status: 'failed', error: 'boom', finished_at: at(10) }), run({ id: 2, status: 'warning' }), run({ id: 1, error: '', finished_at: at(50) })]
  it('lists the runs oldest first, so the newest is at the right', () => {
    expect(healthTicks(runs, T0 + 20_000).map((t) => t.id)).toEqual([1, 2, 3, 4])
  })
  it('gives each run a status, a height by duration and keeps a failed one visible', () => {
    const t = healthTicks(runs, T0 + 20_000)
    expect(t.map((x) => x.status)).toEqual(['ok', 'warning', 'failed', 'running'])
    expect(t[1].h).toBe(1) // the longest run
    expect(t[0].h).toBeCloseTo(0.5)
    expect(t[2].h).toBe(0.45) // 10 s of 100 s would be 0.18: a failed run keeps 45%
  })
  it('shows a queued run low and a cancelled one as cancelled', () => {
    const t = healthTicks([run({ id: 2, status: 'running', queued: true, finished_at: null }), run({ id: 1, status: 'failed', error: 'Cancelled by an admin.' })], T0)
    expect(t.map((x) => x.status)).toEqual(['cancelled', 'queued'])
    expect(t[1].h).toBe(0.3)
  })
  it('takes at most n runs', () => {
    const many = Array.from({ length: 60 }, (_, i) => run({ id: 60 - i }))
    const t = healthTicks(many, T0, 50)
    expect(t).toHaveLength(50)
    expect(t[49].id).toBe(60)
  })
  it('sums up the finished runs: how many OK, the failures and the median of the good ones', () => {
    expect(healthSummary(runs)).toEqual({ total: 3, ok: 1, warning: 1, failed: 1, median: 50_000 })
    expect(healthSummary([])).toEqual({ total: 0, ok: 0, warning: 0, failed: 0, median: null })
  })
})

describe('time axis', () => {
  const steps = [step({ finished_at: at(40) }), step({ seq: 2, started_at: at(40), finished_at: at(95) })]
  it('fills the axis with a finished run', () => {
    expect(axisSpan(run({ finished_at: at(100) }), steps, T0 + 500_000)).toBe(100_000)
  })
  it('gives an open run some air and at least the usual length, so bars do not rescale every second', () => {
    const open = run({ finished_at: null })
    expect(axisSpan(open, [], T0 + 60_000)).toBeCloseTo(63_600)
    expect(axisSpan(open, [], T0 + 60_000, 100_000)).toBeCloseTo(108_000)
    expect(axisSpan(open, steps, T0 + 60_000)).toBeCloseTo(100_700) // a step that ended later than now stretches it
    expect(axisSpan(open, [], T0 + 400_000, 100_000)).toBeCloseTo(424_000) // past the usual: it grows with the run
  })
  it('never collapses to nothing', () => {
    expect(axisSpan(run({ finished_at: at(0) }), [], T0)).toBe(1000)
  })
  it('places a bar as a share of the axis and gives a 0 s step a sliver', () => {
    expect(barOf(step({ started_at: at(25), finished_at: at(50) }), T0, 100_000, T0)).toEqual({ left: 25, width: 25 })
    const sliver = barOf(step({ started_at: at(30), finished_at: at(30) }), T0, 100_000, T0)!
    expect(sliver.left).toBe(30)
    expect(sliver.width).toBe(0.4)
  })
  it('draws no bar for a step that never started, and grows an open one to now', () => {
    expect(barOf(step({ started_at: null, finished_at: null, status: 'skipped' }), T0, 100_000, T0)).toBeNull()
    expect(barOf(step({ started_at: at(10), finished_at: null, status: 'running' }), T0, 100_000, T0 + 60_000)).toEqual({ left: 10, width: 50 })
  })
  it('keeps a bar inside the axis', () => {
    const b = barOf(step({ started_at: at(99), finished_at: at(140) }), T0, 100_000, T0)!
    expect(b.left + b.width).toBeLessThanOrEqual(100)
  })
  it('chooses round gridlines, at most six', () => {
    const t = axisTicks(9 * 60_000)
    expect(t.step).toBe(2 * 60_000)
    expect(t.marks.map((m) => m.ms / 60_000)).toEqual([0, 2, 4, 6, 8])
    expect(axisTicks(40_000).step).toBe(10_000)
    expect(axisTicks(3 * 3600_000).step).toBe(1800_000)
  })
  it('labels the marks and the clock', () => {
    expect(axisLabel(0)).toBe('0')
    expect(axisLabel(90_000)).toBe('1 min 30 s')
    expect(clock(7_000)).toBe('00:07')
    expect(clock(417_000)).toBe('06:57')
    expect(clock(3_735_000)).toBe('1:02:15')
  })
})

describe('steps in order', () => {
  const list = [step({ seq: 1000, kera: true, name: 'Docs' }), step({ seq: 1, name: 'secret-scan' }), step({ seq: 2, name: 'kera' }), step({ seq: 3, name: 'start' }), step({ seq: 1001, kera: true, name: 'Guides' })]
  it('puts the implicit start step first, even when an older run recorded it last', () => {
    expect(scriptSteps(list).map((s) => s.name)).toEqual(['start', 'secret-scan', 'kera'])
  })
  it('lists the collections by seq and the whole run script first', () => {
    expect(keraSteps(list).map((s) => s.name)).toEqual(['Docs', 'Guides'])
    expect(orderedSteps(list).map((s) => s.name)).toEqual(['start', 'secret-scan', 'kera', 'Docs', 'Guides'])
  })
  it('counts the steps that are over, and the skipped ones among them', () => {
    expect(stepsDone([step(), step({ status: 'skipped' }), step({ status: 'running' }), step({ status: 'pending' })])).toEqual({ done: 2, skipped: 1, total: 4 })
  })
  it('opens the running step, a failed one and the one a link points at', () => {
    expect(opensByDefault({ status: 'running', seq: 1 }, null)).toBe(true)
    expect(opensByDefault({ status: 'failed', seq: 1 }, null)).toBe(true)
    expect(opensByDefault({ status: 'ok', seq: 1 }, null)).toBe(false)
    expect(opensByDefault({ status: 'ok', seq: 4 }, 4)).toBe(true)
  })
})

describe('collections', () => {
  it('splits a collection into segments by share, leaving zeros out', () => {
    const s = syncSegments({ listed: 100, unchanged: 90, uploaded: 8, removed: 2, adopted: 0, failed: 0 })
    expect(s.map((x) => x.key)).toEqual(['unchanged', 'uploaded', 'removed'])
    expect(s.map((x) => x.pct)).toEqual([90, 8, 2])
  })
  it('is quiet when nothing moved', () => {
    expect(syncQuiet({ listed: 10, unchanged: 10 })).toBe(true)
    expect(syncQuiet({ listed: 10, unchanged: 9, uploaded: 1 })).toBe(false)
    expect(syncQuiet({ listed: 10, unchanged: 10, waiting: 3 })).toBe(false)
    expect(syncQuiet(undefined)).toBe(true)
  })
})

describe('the sentence of a run', () => {
  const stages = [{ name: 'fetch-itop', title: 'Fetch iTop', about: '' }]
  const running = run({ status: 'running', finished_at: null, client_done: false })
  const s = (o: Partial<Step>) => step(o)
  it('names the running step, with its number', () => {
    const steps = [s({ seq: 1, name: 'fetch-alf' }), s({ seq: 2, name: 'fetch-itop', status: 'running', finished_at: null })]
    expect(runSentence(running, steps, stages, T0 + 60_000)).toBe('Running · step 2 · Fetch iTop')
  })
  it('says how many there are when the script announced them up front', () => {
    const steps = [s({ seq: 1, name: 'fetch-alf', status: 'running', finished_at: null }), s({ seq: 2, name: 'convert', status: 'pending', started_at: null, finished_at: null })]
    expect(runSentence(running, steps, stages, T0)).toBe('Running · step 1 of 2 · Fetch alf')
  })
  it('follows the run into the collections, and waits for a first step', () => {
    const steps = [s({ seq: 1 }), s({ seq: 1000, kera: true, name: 'Docs', status: 'running', finished_at: null }), s({ seq: 1001, kera: true, name: 'Guides', status: 'pending', started_at: null, finished_at: null })]
    expect(runSentence(running, steps, stages, T0)).toBe('Running · syncing collection 1 of 2 · Docs')
    expect(runSentence(running, [], stages, T0)).toBe('Running · waiting for the first step')
  })
  it('says queued and stopping', () => {
    expect(runSentence({ ...running, queued: true }, [], stages, T0)).toBe('Queued · waiting for a runner')
    expect(runSentence({ ...running, cancel_requested: true }, [], stages, T0)).toBe('Stopping · the runner is stopping the script')
  })
  it('says where it failed and why, in one short line', () => {
    const failed = run({ status: 'failed', error: 'fetch-itop failed' })
    const steps = [s({ seq: 1, name: 'fetch-alf' }), s({ seq: 2, name: 'fetch-itop', status: 'failed', error: `exit code 1\n${'x'.repeat(200)}` })]
    expect(runSentence(failed, steps, stages, T0)).toBe('Failed at Fetch iTop · exit code 1')
    expect(runSentence(failed, [], stages, T0)).toBe('Failed · fetch-itop failed')
    expect(runSentence(run({ status: 'failed' }), [], stages, T0)).toBe('Failed')
  })
  it('keeps a long reason to a line', () => {
    expect(firstLine('a'.repeat(100), 20)).toHaveLength(20)
    expect(firstLine('one\ntwo')).toBe('one')
  })
  it('says closed and cancelled in their own words, and the time of a good run', () => {
    expect(runSentence(run({ status: 'failed', error: 'Closed by an admin at 08:12 UTC.' }), [], stages, T0)).toBe('Closed by an admin at 08:12 UTC')
    expect(runSentence(run({ status: 'failed', error: 'Cancelled by an admin.', finished_at: at(75) }), [], stages, T0)).toBe('Cancelled by an admin after 1 min 15 s')
    expect(runSentence(run({ finished_at: at(552) }), [], stages, T0)).toBe('Finished OK in 9 min 12 s')
    expect(runSentence(run({ status: 'warning', finished_at: at(552) }), [], stages, T0)).toBe('Finished with a warning in 9 min 12 s')
    expect(runSentence(run({ dry_run: true }), [], stages, T0)).toBe('Dry run finished in 1 min 40 s')
  })
  it('picks the chip of a run', () => {
    expect(runChip({ status: 'running', queued: true, cancel_requested: false, error: '' })).toBe('queued')
    expect(runChip({ status: 'running', queued: false, cancel_requested: true, error: '' })).toBe('stopping')
    expect(runChip({ status: 'running', queued: false, cancel_requested: false, error: '' })).toBe('running')
    expect(runChip({ status: 'failed', queued: false, cancel_requested: false, error: 'Cancelled by an admin.' })).toBe('cancelled')
    expect(runChip({ status: 'failed', queued: false, cancel_requested: false, error: 'boom' })).toBe('failed')
  })
})
