package main

// Feature packages register their routes in init() via app.Register.
// Import each one here (blank import when main does not call it directly).
import (
	_ "github.com/cdmx-in/kera/internal/identity"
)
