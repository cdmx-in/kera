// Package web embeds the built single-page app (web/dist, produced by `pnpm build`).
package web

import "embed"

// Dist holds the Vite build output. It is empty (only .gitkeep) until the
// frontend is built; the server then serves a placeholder page.
//
//go:embed all:dist
var Dist embed.FS
