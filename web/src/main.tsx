import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router/dom'
import './styles/app.css'
import { queryClient } from './app/query'
import { router } from './app/router'

// ?mock=1 under `pnpm dev` turns the mock on for this tab.
function devMockRequested(): boolean {
  try {
    if (new URLSearchParams(location.search).get('mock') === '1') sessionStorage.setItem('kera:mock', '1')
    return sessionStorage.getItem('kera:mock') === '1'
  } catch {
    return false
  }
}

async function boot() {
  // Mock API for UI work without a backend. Written inline so production
  // builds (DEV false, VITE_MOCK unset) drop the branch and the mock chunk.
  if (import.meta.env.VITE_MOCK === '1' || (import.meta.env.DEV && devMockRequested())) (await import('./api/mock')).installMock()
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  )
}

void boot()
