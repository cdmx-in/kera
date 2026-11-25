// The live half of the script notebook: a session on a runner (cells run in it and keep their state) and the
// checks of the editor's files. The pure parts (cells, problem lines, Run all, polling merge) are in pipelines.ts.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { EditorHandle } from './CodeEditor'
import { api, ApiError, authHeaders } from '@/api/client'
import { enc } from './api'
import { toast } from '@/stores/toast'
import {
  applyCells,
  autoCheckIn,
  cellBusy,
  cellLines,
  isCellHeader,
  joinCells,
  laterStamp,
  lineSeverities,
  locateLine,
  problemsByCell,
  runAllNext,
  sessionLive,
  splitCells,
  stepNames,
  type Cell,
  type CellRun,
  type CellStatus,
  type CheckResult,
  type Problem,
  type SessionOut,
} from './pipelines'

const path = (name: string) => `/api/v1/admin/pipelines/${enc(name)}`
let nextCellId = 1

/** An editor cell: a Cell with a key of its own (the session's runs are looked up by it). */
export type NbCell = Cell & { id: string }
export interface Notebook {
  cells: NbCell[]
  /** the file used CRLF: the cells are shown and edited with LF and written back with CRLF */
  crlf: boolean
}

export const newCell = (c: Partial<Cell> = {}): NbCell => ({ title: '', header: '#%%', code: '', bare: false, ...c, id: `c${nextCellId++}` })

export function nbFromText(text: string): Notebook {
  const crlf = text.includes('\r\n')
  return { crlf, cells: splitCells(crlf ? text.replace(/\r\n/g, '\n') : text).map((c) => ({ ...c, id: `c${nextCellId++}` })) }
}

export function nbText(nb: Notebook): string {
  const t = joinCells(nb.cells)
  return nb.crlf ? t.replace(/\n/g, '\r\n') : t
}

/** Closes a session without waiting for the answer, also while the page is going away (a plain fetch would be cancelled). */
function closeQuietly(name: string, id: string) {
  try {
    void fetch(`${path(name)}/sessions/${id}`, { method: 'DELETE', keepalive: true, credentials: 'same-origin', headers: authHeaders() }).catch(() => undefined)
  } catch {
    // best effort: the server closes an idle session after 20 minutes
  }
}

export interface Session {
  id: string
  status: SessionOut['status']
  error: string
  real: boolean
}

const infoOf = (s: SessionOut): Session => ({ id: s.id, status: s.status, error: s.error, real: s.real_workspace })

export function useNotebookSession(name: string) {
  const [session, setSession] = useState<Session | null>(null)
  const [runs, setRuns] = useState<Record<string, CellRun>>({})
  const [noRunner, setNoRunner] = useState(false)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const live = useRef<{ session: Session | null; since: string; busy: boolean }>({ session: null, since: '', busy: false })
  const opening = useRef<Promise<string | null> | null>(null)

  live.current.session = session
  live.current.busy = !!session && (session.status === 'starting' || session.status === 'closing' || Object.values(runs).some((r) => cellBusy(r.status)))

  // poll: every 600 ms while a cell is queued or running, every 3 s otherwise; only cells changed since the last answer come back
  const id = session && session.status !== 'closed' ? session.id : ''
  useEffect(() => {
    if (!id) return
    let stop = false
    let timer: ReturnType<typeof setTimeout>
    const tick = async () => {
      try {
        const q = live.current.since ? `?since=${encodeURIComponent(live.current.since)}` : ''
        const s = await api<SessionOut>(`${path(name)}/sessions/${id}${q}`)
        if (stop) return
        for (const c of s.cells) live.current.since = laterStamp(live.current.since, c.updated_at)
        setRuns((r) => applyCells(r, s.cells))
        setSession((cur) => (cur && cur.id === s.id && (cur.status !== s.status || cur.error !== s.error) ? infoOf(s) : cur))
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) setSession((cur) => (cur?.id === id ? { ...cur, status: 'closed', error: 'The session is gone' } : cur))
      }
      if (!stop) timer = setTimeout(tick, live.current.busy ? 600 : 3000)
    }
    timer = setTimeout(tick, live.current.busy ? 600 : 1000)
    return () => {
      stop = true
      clearTimeout(timer)
    }
  }, [id, name])

  // leaving the tab or the page ends the session (best effort)
  useEffect(() => {
    const bye = () => {
      const s = live.current.session
      if (sessionLive(s)) closeQuietly(name, s!.id)
    }
    addEventListener('pagehide', bye)
    return () => {
      removeEventListener('pagehide', bye)
      bye()
    }
  }, [name])

  /** Starts a session with these files; the server closes the one that was open. Answers its id, or null (see noRunner and error). */
  const start = useCallback(
    async (files: Record<string, string>, entry: string, real: boolean): Promise<string | null> => {
      if (opening.current) return opening.current
      const go = async () => {
        setStarting(true)
        setNoRunner(false)
        setError('')
        try {
          const s = await api<SessionOut>(`${path(name)}/sessions`, { method: 'POST', json: { files, entry, real_workspace: real } })
          live.current.since = ''
          // a new session forgets the old runs; a cell that is just being sent (no server id yet) is what started it
          setRuns((r) => Object.fromEntries(Object.entries(r).filter(([, v]) => v.server === 0)))
          setSession(infoOf(s))
          return s.id
        } catch (e) {
          if (e instanceof ApiError && e.code === 'no_runner') setNoRunner(true)
          else setError((e as Error).message)
          return null
        } finally {
          setStarting(false)
          opening.current = null
        }
      }
      opening.current = go()
      return opening.current
    },
    [name],
  )

  /** Queues a cell. The editor cell shows Queued at once; a session is started first when there is none. */
  const run = useCallback(
    async (key: string, title: string, code: string, files: Record<string, string>, entry: string, real: boolean): Promise<boolean> => {
      setRuns((r) => ({ ...r, [key]: { server: 0, status: 'queued', output: '', exit_code: null, sent: code } }))
      const fail = (msg: string) => {
        setRuns((r) => {
          const { [key]: _gone, ...rest } = r
          return rest
        })
        if (msg) toast(msg, 'error')
        return false
      }
      let sid = live.current.session && sessionLive(live.current.session) ? live.current.session.id : null
      if (!sid) {
        sid = await start(files, entry, real)
        if (!sid) return fail('')
      }
      try {
        const r = await api<{ cell: number }>(`${path(name)}/sessions/${sid}/cells`, { method: 'POST', json: { name: title, code } })
        setRuns((all) => (all[key] ? { ...all, [key]: { ...all[key], server: r.cell } } : all))
        return true
      } catch (e) {
        if (e instanceof ApiError && e.code === 'session_closed') setSession((cur) => (cur ? { ...cur, status: 'closed' } : cur))
        return fail((e as Error).message)
      }
    },
    [name, start],
  )

  const stop = useCallback(
    async (key: string) => {
      const r = live.current.session && runs[key]
      if (!r || !live.current.session || !r.server) return
      try {
        const s = await api<SessionOut>(`${path(name)}/sessions/${live.current.session.id}/cells/${r.server}/stop`, { method: 'POST' })
        setRuns((all) => applyCells(all, s.cells))
      } catch (e) {
        toast((e as Error).message, 'error')
      }
    },
    [name, runs],
  )

  const close = useCallback(async () => {
    const s = live.current.session
    if (!s) return
    try {
      const out = await api<SessionOut>(`${path(name)}/sessions/${s.id}`, { method: 'DELETE' })
      setSession(infoOf(out))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }, [name])

  const clearOutput = useCallback((key: string) => setRuns((r) => (r[key] ? { ...r, [key]: { ...r[key], output: '' } } : r)), [])
  const forget = useCallback((key: string) => setRuns((r) => (key in r ? Object.fromEntries(Object.entries(r).filter(([k]) => k !== key)) : r)), [])

  return { session, runs, noRunner, starting, error, start, run, stop, close, clearOutput, forget }
}

/** Checks the editor's files on a runner (syntax, ShellCheck): by hand, and about 1.5 s after the last edit when a runner is online. Never blocks typing. */
export function useChecks(name: string, files: Record<string, string>, entry: string, online: boolean | undefined) {
  const [result, setResult] = useState<CheckResult | null>(null)
  const [checking, setChecking] = useState(false)
  const [hint, setHint] = useState('')
  const [checkedAt, setCheckedAt] = useState(0)
  const [editedAt, setEditedAt] = useState(() => Date.now())
  const latest = useRef(0)
  const body = useMemo(() => ({ files, entry }), [files, entry])

  // any change to the files counts as an edit
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    setEditedAt(Date.now())
  }, [body])

  const check = useCallback(async () => {
    if (!Object.keys(body.files).length || !body.entry) return
    const n = ++latest.current
    setChecking(true)
    setCheckedAt(Date.now()) // an edit during the check makes the next one due
    try {
      const r = await api<CheckResult>(`${path(name)}/check`, { method: 'POST', json: body })
      if (n !== latest.current) return
      setResult(r)
      setHint(r.error ?? '')
    } catch (e) {
      if (n !== latest.current) return
      setHint(e instanceof ApiError && e.code === 'no_runner' ? 'no_runner' : e instanceof ApiError && e.code === 'check_timeout' ? 'The runner did not answer in time' : (e as Error).message)
    } finally {
      if (n === latest.current) setChecking(false)
    }
  }, [name, body])

  const bodyRef = useRef(check)
  bodyRef.current = check
  useEffect(() => {
    const wait = autoCheckIn({ online, checking, editedAt, checkedAt, now: Date.now(), entry })
    if (wait === null) return
    const t = setTimeout(() => void bodyRef.current(), wait)
    return () => clearTimeout(t)
  }, [online, checking, editedAt, checkedAt, entry])

  return { problems: result?.problems ?? [], tools: result?.tools ?? [], checking, hint, check, checked: result !== null }
}

/**
 * Everything the notebook view needs on top of the draft: the cells of the entry file (kept in step with its text), the session,
 * Run all, the checks and where a clicked problem goes. The cells are the script: every edit writes the whole entry file back.
 */
export function useNotebookController(o: { name: string; files: Record<string, string>; entry: string; file: string; setEntryText: (t: string) => void; setActive: (f: string) => void; online: boolean | undefined; enabled: boolean }) {
  const { name, files, entry, file, setEntryText, setActive, online, enabled } = o
  const text = files[entry] ?? ''
  const [nb, setNb] = useState<Notebook>(() => nbFromText(text))
  const [raw, setRaw] = useState(false)
  const [real, setReal] = useState(false)
  const [runAll, setRunAll] = useState<{ ids: string[]; cursor: number } | null>(null)
  const [focusId, setFocusId] = useState<string | null>(null)
  const [pendingLine, setPendingLine] = useState<number | null>(null)
  const lastText = useRef<string | null>(text) // the text the cells were made from or write
  const cells = useRef(new Map<string, EditorHandle>())
  const titles = useRef(new Map<string, HTMLInputElement>())
  const plain = useRef<EditorHandle>(null)
  const sess = useNotebookSession(name)
  const checks = useChecks(name, files, entry, online)
  const cellsVisible = enabled && file === entry && !raw

  // the entry text changed under the cells (a version was restored, changes discarded, the raw file edited): split again. In the raw view the cells wait.
  useEffect(() => {
    if (raw || text === lastText.current) return
    lastText.current = text
    setNb(nbFromText(text))
  }, [text, raw])

  const commit = (next: NbCell[]) => {
    const n = { crlf: nb.crlf, cells: next }
    const t = nbText(n)
    setNb(n)
    setEntryText(t)
    // a "#%%" line typed into a cell makes a new one: split the file again
    lastText.current = next.some((c) => c.code.split('\n').some(isCellHeader)) ? null : t
  }
  const edit = (i: number, patch: Partial<Cell>) => commit(nb.cells.map((c, k) => (k === i ? { ...c, ...patch } : c)))
  const addBelow = (i: number) => {
    const c = newCell()
    commit([...nb.cells.slice(0, i + 1), c, ...nb.cells.slice(i + 1)])
    return c
  }
  const remove = (i: number) => {
    if (nb.cells.length < 2) return
    sess.forget(nb.cells[i].id)
    commit(nb.cells.filter((_, k) => k !== i))
  }
  const canMove = (i: number, by: -1 | 1) => {
    const j = i + by
    if (j < 0 || j >= nb.cells.length) return false
    return nb.cells[i].header !== null && nb.cells[j].header !== null // the setup cell stays first
  }
  const move = (i: number, by: -1 | 1) => {
    if (!canMove(i, by)) return
    const next = [...nb.cells]
    ;[next[i], next[i + by]] = [next[i + by], next[i]]
    commit(next)
  }

  const nameOf = (c: NbCell, i: number) => c.title || (c.header === null ? 'setup' : (stepNames(nb.cells)[i] ?? ''))
  const runById = (id: string) => {
    const i = nb.cells.findIndex((c) => c.id === id)
    if (i < 0) return Promise.resolve(false)
    return sess.run(id, nameOf(nb.cells[i], i), nb.cells[i].code, files, entry, real)
  }
  const runCell = (i: number) => void runById(nb.cells[i].id)
  const runUpTo = (i: number) => setRunAll({ ids: nb.cells.slice(0, i + 1).map((c) => c.id), cursor: -1 })
  const startRunAll = () => setRunAll({ ids: nb.cells.map((c) => c.id), cursor: -1 })
  const stopAll = () => {
    setRunAll(null)
    for (const [k, r] of Object.entries(sess.runs)) if (cellBusy(r.status)) void sess.stop(k)
  }
  const start = async () => {
    setRunAll(null)
    await sess.start(files, entry, real)
  }
  const runNext = (i: number) => {
    runCell(i)
    if (i + 1 < nb.cells.length) setFocusId(nb.cells[i + 1].id)
    else setFocusId(addBelow(i).id)
  }

  // Run all: one cell at a time, so that a failure stops the rest
  const statuses = useMemo(() => Object.fromEntries(Object.entries(sess.runs).map(([k, r]) => [k, r.status])) as Record<string, CellStatus | undefined>, [sess.runs])
  useEffect(() => {
    if (!runAll) return
    const step = runAllNext(runAll.ids, statuses, runAll.cursor)
    if (step.action === 'wait') return
    if (step.action === 'run') {
      setRunAll({ ...runAll, cursor: step.index })
      void runById(runAll.ids[step.index])
      return
    }
    setRunAll(null)
    if (step.action === 'stop' && step.reason !== 'lost') {
      const at = nb.cells.findIndex((c) => c.id === runAll.ids[step.index])
      if (step.reason === 'failed') toast(`Run all stopped: cell ${at + 1} failed`, 'error')
      else toast(`Run all stopped at cell ${at + 1}`)
    }
  }, [runAll, statuses]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (focusId && cells.current.get(focusId)) {
      cells.current.get(focusId)?.focus()
      setFocusId(null)
    }
  }, [focusId, nb.cells])
  useEffect(() => {
    if (pendingLine !== null && plain.current) {
      plain.current.focusLine(pendingLine)
      setPendingLine(null)
    }
  }, [pendingLine, file, raw])

  /** A problem was clicked: the cell and line when the entry is shown as cells, else the file's editor. */
  const pick = (p: Problem) => {
    if (cellsVisible && p.file === entry) {
      const at = locateLine(nb.cells, p.line)
      if (!at) return
      const id = nb.cells[at.cell].id
      if (at.line === 0) titles.current.get(id)?.focus()
      else cells.current.get(id)?.focusLine(at.line)
      return
    }
    if (p.file !== file) setActive(p.file)
    setPendingLine(p.line)
  }

  return {
    nb,
    raw,
    setRaw,
    real,
    setReal,
    sess,
    checks,
    cellsVisible,
    runningAll: runAll !== null,
    starts: cellLines(nb.cells),
    steps: stepNames(nb.cells),
    byCell: problemsByCell(nb.cells, checks.problems, entry),
    marks: lineSeverities(checks.problems, file),
    refs: { cells, titles, plain },
    edit,
    addBelow,
    remove,
    canMove,
    move,
    runCell,
    runUpTo,
    runAll: startRunAll,
    stopAll,
    start,
    runNext,
    pick,
  }
}
