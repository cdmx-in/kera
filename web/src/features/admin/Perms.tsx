import { Switch } from '@/ui/misc'
import { humanize, permRows, setPerm, type Perms } from './lib'

/** Grouped switches over the known permission tree. */
export function PermSwitches({ known, value, onChange, idPrefix = 'perm' }: { known: Perms; value: Perms; onChange: (p: Perms) => void; idPrefix?: string }) {
  return (
    <div className="grid gap-4 sm:grid-cols-3">
      {permRows(known, value).map(({ cat, leaves }) => (
        <fieldset key={cat} className="space-y-2">
          <legend className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted">{humanize(cat)}</legend>
          {leaves.map((l) => (
            <div key={l.key} className="flex items-center justify-between gap-3 text-sm">
              <span>{humanize(l.key)}</span>
              <Switch checked={l.on} onChange={(on) => onChange(setPerm(value, cat, l.key, on))} label={`${humanize(cat)}: ${humanize(l.key)}`} testId={`${idPrefix}-${cat}-${l.key}`} />
            </div>
          ))}
        </fieldset>
      ))}
    </div>
  )
}
