// Lazy chunk: install/update steps for a browser extension.
import { useState } from 'react'
import { Check, Copy, Download, Monitor, Terminal } from 'lucide-react'
import type { ExtensionInstallIntegration } from '@/lib/ui-config'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Markdown } from '@/features/chat/markdown/Markdown'
import { cx } from '@/lib/format'
import { uaData } from './extension'

export function fillPlaceholders(text: string, vars: Record<string, string>): string {
  return text.replace(/\{\{\s*(\w+)\s*\}\}/g, (m, k: string) => vars[k] ?? m)
}

const fallback = {
  install: 'Version {{version}}.\n\n1. [Download the extension]({{downloadUrl}}) and unzip it to a folder named `{{folder}}`.\n2. Open `chrome://extensions` and switch on **Developer mode**.\n3. Click **Load unpacked** and pick the `{{folder}}` folder.',
  update: 'Version {{version}} is available.\n\n1. [Download the update]({{downloadUrl}}) and unzip it over your `{{folder}}` folder.\n2. Open `chrome://extensions` and click the reload button on the extension.',
}

type OS = 'windows' | 'posix'

export default function ExtensionDialog({ integration: i, mode, onClose }: { integration: ExtensionInstallIntegration; mode: 'install' | 'update'; onClose: () => void }) {
  const detected: OS = /win/i.test(uaData()?.platform ?? navigator.platform ?? '') ? 'windows' : 'posix'
  const available = (['windows', 'posix'] as OS[]).filter((o) => i.commands?.[o])
  const [os, setOS] = useState<OS>(available.includes(detected) ? detected : (available[0] ?? detected))
  const folder = (i.downloadUrl.split('/').pop() ?? i.id).replace(/\.zip$/i, '')
  // A terminal needs an absolute URL; the config stores a site-relative path.
  const absolute = new URL(i.downloadUrl, location.origin).href
  const command = fillPlaceholders(i.commands?.[os] ?? '', { version: i.version, downloadUrl: absolute, folder })
  // Steps that already embed {{command}} get it inline; otherwise the command card below shows it.
  const template = i.steps?.[mode] ?? fallback[mode]
  const inline = /\{\{\s*command\s*\}\}/.test(template)
  const steps = fillPlaceholders(template, { version: i.version, downloadUrl: i.downloadUrl, folder, command })
  const title = `${mode === 'update' ? 'Update' : 'Install'} ${i.label}`
  return (
    <Dialog open onClose={onClose} label={title} className="w-[min(44rem,calc(100vw-2rem))]">
      <DialogHeader title={title} onClose={onClose} />
      <div className="max-h-[72dvh] space-y-4 overflow-y-auto px-5 py-4 text-sm">
        <Markdown text={steps} />
        {!inline && available.length > 0 && (
          <section className="overflow-hidden rounded-xl border border-line" data-testid="install-command">
            <div className="flex items-center gap-1 border-b border-line bg-surface-2/50 px-2 py-1.5">
              <Terminal size={14} className="ml-1 mr-1 text-muted" aria-hidden />
              {available.map((o) => (
                <button
                  key={o}
                  type="button"
                  onClick={() => setOS(o)}
                  aria-pressed={os === o}
                  className={cx('inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium', os === o ? 'bg-surface text-fg shadow-sm' : 'text-muted hover:text-fg')}
                >
                  <Monitor size={12} aria-hidden />
                  {o === 'windows' ? 'Windows (PowerShell)' : 'macOS / Linux'}
                  {o === detected && <span className="text-[10px] text-accent dark:text-accent-bright">· yours</span>}
                </button>
              ))}
              <span className="flex-1" />
              <CopyButton text={command} />
            </div>
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all bg-surface px-3 py-2.5 font-mono text-xs leading-relaxed">{command}</pre>
          </section>
        )}
      </div>
      <div className="flex items-center justify-between gap-3 border-t border-line px-5 py-3">
        <span className="text-xs text-muted">Version {i.version}</span>
        <a href={i.downloadUrl} download className="inline-flex items-center gap-1.5 rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white hover:opacity-90">
          <Download size={14} /> Download {i.version}
        </a>
      </div>
    </Dialog>
  )
}

function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      onClick={() => {
        void navigator.clipboard.writeText(text).then(() => {
          setDone(true)
          setTimeout(() => setDone(false), 1500)
        })
      }}
      className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted hover:bg-surface hover:text-fg"
      data-testid="copy-command"
    >
      {done ? <Check size={13} className="text-accent" /> : <Copy size={13} />}
      {done ? 'Copied' : 'Copy'}
    </button>
  )
}
