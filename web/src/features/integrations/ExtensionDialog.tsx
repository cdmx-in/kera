// Lazy chunk: install/update steps for a browser extension.
import { Download } from 'lucide-react'
import type { ExtensionInstallIntegration } from '@/lib/ui-config'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Markdown } from '@/features/chat/markdown/Markdown'
import { uaData } from './extension'

export function fillPlaceholders(text: string, vars: Record<string, string>): string {
  return text.replace(/\{\{\s*(\w+)\s*\}\}/g, (m, k: string) => vars[k] ?? m)
}

const fallback = {
  install: 'Version {{version}}.\n\n1. [Download the extension]({{downloadUrl}}) and unzip it to a folder named `{{folder}}`.\n2. Open `chrome://extensions` and switch on **Developer mode**.\n3. Click **Load unpacked** and pick the `{{folder}}` folder.',
  update: 'Version {{version}} is available.\n\n1. [Download the update]({{downloadUrl}}) and unzip it over your `{{folder}}` folder.\n2. Open `chrome://extensions` and click the reload button on the extension.',
}

export default function ExtensionDialog({ integration: i, mode, onClose }: { integration: ExtensionInstallIntegration; mode: 'install' | 'update'; onClose: () => void }) {
  const windows = /win/i.test(uaData()?.platform ?? navigator.platform ?? '')
  const folder = (i.downloadUrl.split('/').pop() ?? i.id).replace(/\.zip$/i, '')
  const vars = { version: i.version, downloadUrl: i.downloadUrl, folder, command: (windows ? i.commands?.windows : i.commands?.posix) ?? '' }
  const steps = fillPlaceholders(i.steps?.[mode] ?? fallback[mode], vars)
  return (
    <Dialog open onClose={onClose} label={`${mode === 'update' ? 'Update' : 'Install'} ${i.label}`} className="w-[min(36rem,calc(100vw-2rem))]">
      <DialogHeader title={`${mode === 'update' ? 'Update' : 'Install'} ${i.label}`} onClose={onClose} />
      <div className="max-h-[70dvh] overflow-y-auto px-5 py-4 text-sm">
        <Markdown text={steps} />
      </div>
      <div className="flex justify-end border-t border-line px-5 py-3">
        <a href={i.downloadUrl} download className="inline-flex items-center gap-1.5 rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white">
          <Download size={14} /> Download {i.version}
        </a>
      </div>
    </Dialog>
  )
}
