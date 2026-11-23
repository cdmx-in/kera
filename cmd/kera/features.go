package main

// Feature packages register their routes in init() via app.Register.
// Import each one here (blank import when main does not call it directly).
import (
	_ "github.com/cdmx-in/kera/internal/admin"
	_ "github.com/cdmx-in/kera/internal/chats"
	_ "github.com/cdmx-in/kera/internal/cowork"
	_ "github.com/cdmx-in/kera/internal/desktop"
	_ "github.com/cdmx-in/kera/internal/engine"
	_ "github.com/cdmx-in/kera/internal/files"
	_ "github.com/cdmx-in/kera/internal/identity"
	_ "github.com/cdmx-in/kera/internal/knowledge"
	_ "github.com/cdmx-in/kera/internal/memory"
	_ "github.com/cdmx-in/kera/internal/models"
	_ "github.com/cdmx-in/kera/internal/oauthclient"
	_ "github.com/cdmx-in/kera/internal/pipeline"
	_ "github.com/cdmx-in/kera/internal/prompts"
	_ "github.com/cdmx-in/kera/internal/sandbox"
	_ "github.com/cdmx-in/kera/internal/schedule"
	_ "github.com/cdmx-in/kera/internal/skills"
	_ "github.com/cdmx-in/kera/internal/toolserver"
	_ "github.com/cdmx-in/kera/internal/ui"
	_ "github.com/cdmx-in/kera/internal/websearch"
)
