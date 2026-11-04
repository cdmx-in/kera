package main

// Feature packages register their routes in init() via app.Register.
// Import each one here (blank import when main does not call it directly).
import (
	_ "github.com/cdmx-in/kera/internal/admin"
	_ "github.com/cdmx-in/kera/internal/chats"
	_ "github.com/cdmx-in/kera/internal/engine"
	_ "github.com/cdmx-in/kera/internal/files"
	_ "github.com/cdmx-in/kera/internal/identity"
	_ "github.com/cdmx-in/kera/internal/memory"
	_ "github.com/cdmx-in/kera/internal/models"
	_ "github.com/cdmx-in/kera/internal/sandbox"
	_ "github.com/cdmx-in/kera/internal/ui"
	_ "github.com/cdmx-in/kera/internal/websearch"
)
