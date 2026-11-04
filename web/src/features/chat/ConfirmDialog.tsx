import { useState } from 'react'
import type { PendingCall } from '@/api/types'
import { toast } from '@/stores/toast'
import { Dialog } from '@/ui/Dialog'
import { Markdown } from './markdown/Markdown'
import { answerCall } from './stream'

type Confirm = Extract<PendingCall, { kind: 'confirm' }>

/** Tool confirmation; the answer goes over HTTP so a dropped stream cannot lose it. */
export function ConfirmDialog({ messageId, call }: { messageId: string; call: Confirm }) {
  const [busy, setBusy] = useState(false)
  const answer = async (value: boolean) => {
    setBusy(true)
    try {
      await answerCall(messageId, call.callId, value)
    } catch (e) {
      toast(`Could not send the answer: ${(e as Error).message}`, 'error')
      setBusy(false)
    }
  }
  return (
    <Dialog open persistent onClose={() => undefined} testId="confirm-dialog" label={call.title ?? 'Confirm'} className="w-[min(52rem,calc(100vw-2rem))]">
      <div className="flex max-h-[calc(100dvh-2rem)] flex-col">
        <h2 className="border-b border-line px-5 py-3 text-base font-semibold">{call.title ?? 'Confirm'}</h2>
        <div className="max-h-[62dvh] overflow-y-auto px-5 py-4 text-sm">
          <Markdown text={call.message} />
        </div>
        <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
          <button type="button" data-testid="cancel" disabled={busy} onClick={() => answer(false)} className="rounded-lg px-3 py-1.5 text-sm hover:bg-surface-2">
            {call.cancelLabel ?? 'Cancel'}
          </button>
          <button type="button" data-testid="confirm" disabled={busy} onClick={() => answer(true)} className="rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50">
            {call.confirmLabel ?? 'Confirm'}
          </button>
        </div>
      </div>
    </Dialog>
  )
}
