// Voice dictation with the browser's speech recognition, on the device only. Chrome's usual
// recognition streams the audio to Google; with processLocally the audio stays on the machine,
// so a browser that cannot do that gets no microphone button at all.

export type Availability = 'unavailable' | 'downloadable' | 'downloading' | 'available'

interface Recognition {
  lang: string
  continuous: boolean
  interimResults: boolean
  processLocally: boolean
  onresult: ((e: ResultEvent) => void) | null
  onerror: ((e: { error: string }) => void) | null
  onend: (() => void) | null
  start(): void
  stop(): void
}
interface ResultEvent {
  resultIndex: number
  results: ArrayLike<ArrayLike<{ transcript: string }> & { isFinal: boolean }>
}
interface Options {
  langs: string[]
  processLocally: boolean
}
interface RecognitionCtor {
  new (): Recognition
  available?(o: Options): Promise<Availability>
  install?(o: Options): Promise<boolean>
}
type Win = { SpeechRecognition?: RecognitionCtor; webkitSpeechRecognition?: RecognitionCtor }

const ctor = (w: Win) => w.SpeechRecognition ?? w.webkitSpeechRecognition

/** Whether this browser can recognise speech on the device in lang; anything else hides the button. */
export async function dictationStatus(lang: string, w: Win = window as unknown as Win): Promise<Availability> {
  const R = ctor(w)
  if (!R?.available) return 'unavailable'
  try {
    return await R.available({ langs: [lang], processLocally: true })
  } catch {
    return 'unavailable'
  }
}

/** Downloads the on-device language pack; true when it is ready. */
export async function installDictation(lang: string, w: Win = window as unknown as Win): Promise<boolean> {
  const R = ctor(w)
  if (!R?.install) return false
  try {
    return await R.install({ langs: [lang], processLocally: true })
  } catch {
    return false
  }
}

/**
 * Starts listening; each finished phrase goes to onText. onEnd runs when recognition stops, with a
 * message when it stopped because of a problem. Returns a function that stops it.
 */
export function startDictation(lang: string, onText: (text: string) => void, onEnd: (problem?: string) => void, w: Win = window as unknown as Win): () => void {
  const R = ctor(w)
  if (!R) {
    onEnd('Speech recognition is not available in this browser')
    return () => undefined
  }
  const r = new R()
  r.processLocally = true // before start(): the audio must never go to a server
  r.lang = lang
  r.continuous = true
  r.interimResults = false
  let problem: string | undefined
  r.onresult = (e) => {
    for (let i = e.resultIndex; i < e.results.length; i++) {
      if (e.results[i].isFinal) onText(e.results[i][0].transcript.trim())
    }
  }
  r.onerror = (e) => {
    problem = e.error === 'not-allowed' ? 'Microphone access was refused' : e.error === 'no-speech' || e.error === 'aborted' ? undefined : `Dictation stopped: ${e.error}`
  }
  r.onend = () => onEnd(problem)
  r.start()
  return () => r.stop()
}
