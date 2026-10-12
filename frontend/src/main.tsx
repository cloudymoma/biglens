import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'

// When the frontend is rebuilt while a browser tab is already open, lazy-loaded
// chunks (e.g. GraphCanvas) get a new content hash and the old chunk URL 404s.
// Reload once so the tab picks up the new index.html and bundle hashes.
window.addEventListener('vite:preloadError', (event) => {
  const key = 'biglens:preload-reload-at'
  const last = Number(sessionStorage.getItem(key) || 0)
  if (Date.now() - last > 10_000) {
    event.preventDefault()
    sessionStorage.setItem(key, String(Date.now()))
    window.location.reload()
  }
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)

