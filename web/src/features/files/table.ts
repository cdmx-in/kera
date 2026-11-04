// Turns the server's preview rows (already parsed strings) into a rectangular
// table: first row is the header, ragged rows are padded, blank trailing
// columns are dropped, and missing header cells get spreadsheet letters.

export interface TableModel {
  header: string[]
  rows: string[][]
  columns: number
}

export function columnLetter(i: number): string {
  let s = ''
  let n = i + 1
  while (n > 0) {
    const r = (n - 1) % 26
    s = String.fromCharCode(65 + r) + s
    n = Math.floor((n - 1) / 26)
  }
  return s
}

export function tableModel(input: unknown): TableModel {
  const raw = Array.isArray(input) ? input.filter(Array.isArray).map((r) => (r as unknown[]).map((c) => (c == null ? '' : String(c)))) : []
  let columns = 0
  for (const r of raw) {
    for (let i = r.length - 1; i >= 0; i--) {
      if (r[i] !== '') {
        columns = Math.max(columns, i + 1)
        break
      }
    }
  }
  const pad = (r: string[]) => Array.from({ length: columns }, (_, i) => r[i] ?? '')
  const [first = [], ...rest] = raw
  const header = pad(first).map((h, i) => h || columnLetter(i))
  return { header, rows: rest.map(pad), columns }
}
