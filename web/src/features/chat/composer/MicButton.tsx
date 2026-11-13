import { useEffect, useRef, useState } from 'react'
import { Mic } from 'lucide-react'
import { toast } from '@/stores/toast'
import { cx } from '@/lib/format'
import { IconButton } from '@/ui/Dialog'
import { dictationStatus, installDictation, startDictation, type Availability } from './dictation'

/**
 * Dictation into the message box. It appears only when the browser can recognise speech on the
 * device (the audio never leaves the machine); in any other browser there is no button.
 */
export function MicButton({ onText }: { onText: (text: string) => void }) {
  const lang = navigator.language || 'en-US'
  const [status, setStatus] = useState<Availability>('unavailable')
  const [listening, setListening] = useState(false)
  const stop = useRef<(() => void) | null>(null)
  useEffect(() => {
    let live = true
    void dictationStatus(lang).then((s) => live && setStatus(s))
    return () => {
      live = false
      stop.current?.()
    }
  }, [lang])
  if (status !== 'available' && status !== 'downloadable') return null

  const toggle = async () => {
    if (listening) return stop.current?.()
    if (status === 'downloadable') {
      toast('Downloading the speech model for dictation on this device…')
      if (!(await installDictation(lang))) return toast('Could not install on-device speech recognition', 'error')
      setStatus('available')
    }
    setListening(true)
    stop.current = startDictation(lang, onText, (problem) => {
      setListening(false)
      stop.current = null
      if (problem) toast(problem, 'error')
    })
  }
  return (
    <IconButton
      label={listening ? 'Stop dictation' : 'Dictate (the audio stays on this device)'}
      data-testid="dictate"
      aria-pressed={listening}
      onClick={() => void toggle()}
      className={cx('size-9 rounded-full', listening && 'bg-red-500/15 text-red-600 hover:bg-red-500/20')}
    >
      <Mic size={18} className={cx(listening && 'animate-pulse')} />
    </IconButton>
  )
}
