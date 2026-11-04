import { useEffect, useState } from 'react'

export function App() {
  const [version, setVersion] = useState<string>('')
  useEffect(() => {
    fetch('/api/version')
      .then((r) => r.json())
      .then((d: { version: string }) => setVersion(d.version))
      .catch(() => setVersion('unreachable'))
  }, [])
  return (
    <main className="grid min-h-dvh place-items-center bg-[var(--bg)] text-[var(--fg)]">
      <p data-testid="app-ready">Kera {version}</p>
    </main>
  )
}
