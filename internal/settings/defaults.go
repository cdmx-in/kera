package settings

import "encoding/json"

// Defaults are the values used when a key has no row. Every runtime setting
// the code reads should be listed here so `kera export` can show the full set.
var Defaults = map[string]json.RawMessage{
	"site.name":           raw(`"Kera"`),
	"site.timezone":       raw(`"UTC"`),
	"site.default_models": raw(`[]`),
	"site.model_order":    raw(`[]`),
	"site.suggestions":    raw(`[]`),

	"auth.login_form":            raw(`false`), // unused: the sign-in form follows auth.password.enabled
	"auth.signup.oauth":          raw(`true`),
	"auth.signup.password":       raw(`false`),
	"auth.signup.default_role":   raw(`"user"`),
	"auth.jwt_expiry_hours":      raw(`720`),
	"auth.oauth.merge_by_email":  raw(`true`),
	"auth.oauth.update_picture":  raw(`true`),
	"auth.oauth.allowed_domains": raw(`[]`),

	// null = automatic: password sign-in is on while no OAuth login provider exists and off once one does;
	// true or false overrides that (identity.passwordEnabled).
	"auth.password.enabled": raw(`null`),

	// Default permissions for every non-admin user; groups can only add.
	"user.permissions": raw(`{
		"features": {"api_keys": false, "web_search": true, "code_interpreter": true, "memory": true, "temporary_chat": true, "cowork": true, "cowork_schedule": false},
		"chat": {"file_upload": true, "share": true, "export": true, "delete": true},
		"workspace": {"models": false, "knowledge": false, "prompts": false}
	}`),

	"tasks.model_external": raw(`""`),
	"tasks.model_local":    raw(`""`),
	"tasks.title":          raw(`true`),
	"tasks.tags":           raw(`true`),
	"tasks.follow_ups":     raw(`true`),
	"tasks.query":          raw(`true`),

	"chat.max_tool_iterations":  raw(`30`),
	"chat.local_lock":           raw(`false`),
	"chat.compaction.enable":    raw(`false`),
	"chat.compaction.threshold": raw(`120000`),
	"chat.compaction.retention": raw(`30`),
	"chat.memory.enable":        raw(`false`),
	"chat.memory.review_every":  raw(`5`),
	"chat.memory.local_only":    raw(`true`),
	"compat.throwaway_chats":    raw(`true`),

	// Guardrails on every model call. Modes: off | log | redact | block (output_pii: off | log | redact;
	// injection and moderation: off | log | block). Audit-first defaults: nothing changes until an admin flips a mode.
	"guardrails.pii":              raw(`"log"`),
	"guardrails.output_pii":       raw(`"off"`),
	"guardrails.injection":        raw(`"log"`),
	"guardrails.blocklist":        raw(`[]`),
	"guardrails.moderation":       raw(`"off"`),
	"guardrails.moderation.model": raw(`"omni-moderation-latest"`),
	"guardrails.local_exempt":     raw(`true`),

	// Cowork (task mode): the prompt added after the preset prompt ("" = cowork.DefaultPrompt) and its tool-round cap.
	"cowork.prompt":              raw(`""`),
	"cowork.max_tool_iterations": raw(`100`),
	// Scheduled tasks (internal/schedule): saved tasks per user, scheduled runs at once (0 pauses every schedule),
	// and the minutes after which an unattended run is stopped.
	"cowork.schedule.max_per_user":   raw(`5`),
	"cowork.schedule.max_concurrent": raw(`2`),
	"cowork.schedule.max_minutes":    raw(`120`),
	// Desktop sidecar live view: its API and websockify base URLs ("" = off); desktop.key is a secret without default.
	"desktop.url":      raw(`""`),
	"desktop.view_url": raw(`""`),

	// Integrations (internal/connectors): each runs every interval_minutes while enabled.
	// connectors.google_workspace.credentials (service-account JSON) is a secret without default.
	"connectors.google_workspace.enabled":           raw(`false`),
	"connectors.google_workspace.interval_minutes":  raw(`60`),
	"connectors.google_workspace.admin_email":       raw(`""`),
	"connectors.google_workspace.oauth_provider":    raw(`"google"`),
	"connectors.google_workspace.exclude":           raw(`[]`),
	"connectors.google_workspace.delete_after_days": raw(`30`),

	"rag.chunk_size":             raw(`1000`),
	"rag.chunk_overlap":          raw(`100`),
	"rag.markdown_headers":       raw(`true`),
	"rag.top_k":                  raw(`30`),
	"rag.top_k_rerank":           raw(`6`),
	"rag.relevance_threshold":    raw(`0`),
	"rag.hybrid.bm25_weight":     raw(`0.5`),
	"rag.full_context_max_chars": raw(`400000`),
	"rag.fts_config":             raw(`"english"`), // text-search config of new collections (a Postgres regconfig)

	"web.search.engine":             raw(`"duckduckgo"`),
	"web.search.result_count":       raw(`5`),
	"web.search.max_calls_per_turn": raw(`3`),
	"web.fetch.max_chars":           raw(`30000`),
	"web.fetch.max_calls_per_turn":  raw(`3`),
	"web.search.url":                raw(`""`), // searxng base URL; web.search.api_key (brave) is a secret without default
	"web.fetch.allow_private":       raw(`false`),

	// Added for `kera apply` (internal/apply). Secrets apply writes as {"enc": ...}
	// (code_interpreter.token, spend.source.dsn) have no default; read them with apply.ReadSecret.
	"site.url":                     raw(`""`),
	"models.base_public":           raw(`false`), // raw discovered models usable by every user without a grant
	"ui.brand":                     raw(`{}`),
	"ui.tool_display":              raw(`{}`),
	"ui.integrations":              raw(`[]`),
	"rag.embedding.connection":     raw(`""`),
	"rag.embedding.model":          raw(`"nomic-embed-text"`), // the usual small Ollama embedding model; connection "" means "ollama"
	"rag.embedding.dim":            raw(`0`),
	"rag.embedding.batch":          raw(`32`),
	"rag.reranker.url":             raw(`""`),
	"rag.reranker.model":           raw(`""`),
	"rag.reranker.local_runs":      raw(`"skip"`), // skip | allow: local-model runs and an external reranker (rag.reranker.api_key is a secret without default)
	"rag.reranker.local_url":       raw(`""`),
	"rag.extractor.url":            raw(`""`),
	"files.max_mb":                 raw(`100`), // upload limit per file
	"files.workers":                raw(`2`),   // extraction workers; a change needs a restart
	"rag.template":                 raw(`""`),
	"code_interpreter.engine":      raw(`""`),
	"code_interpreter.url":         raw(`""`),
	"code_interpreter.timeout":     raw(`60`),
	"code_interpreter.shared_dir":  raw(`""`),
	"code_interpreter.kernel_dir":  raw(`"/mnt/data"`),
	"code_interpreter.prompt":      raw(`""`),
	"code_interpreter.prelude":     raw(`""`),
	"code_interpreter.max_file_mb": raw(`50`),
	// Languages for execute_code's language argument; only those whose program exists in the sandbox are offered.
	"code_interpreter.languages": raw(`{
		"python": {"label": "Python", "aliases": ["py", "python3"]},
		"javascript": {"label": "JavaScript (Node.js)", "ext": ".js", "command": ["node", "{file}"], "aliases": ["js", "node", "nodejs"]},
		"typescript": {"label": "TypeScript (Node.js)", "ext": ".ts", "aliases": ["ts"],
			"command": ["node", "--experimental-strip-types", "--disable-warning=ExperimentalWarning", "{file}"],
			"check": ["node", "--experimental-strip-types", "-e", ""], "fallback": ["tsx", "{file}"]},
		"php": {"label": "PHP", "ext": ".php", "command": ["php", "{file}"]},
		"bash": {"label": "Bash", "ext": ".sh", "command": ["bash", "{file}"], "aliases": ["shell", "sh"]},
		"ruby": {"label": "Ruby", "ext": ".rb", "command": ["ruby", "{file}"], "aliases": ["rb"]},
		"go": {"label": "Go", "ext": ".go", "command": ["env", "GOTMPDIR={dir}", "go", "run", "{file}"], "aliases": ["golang"]},
		"java": {"label": "Java (single-file source)", "ext": ".java", "command": ["java", "{file}"]},
		"r": {"label": "R", "ext": ".R", "command": ["Rscript", "{file}"], "aliases": ["rscript"]}
	}`),
	"spend.source.kind": raw(`""`),
	// Cap on one tool result passed to the model (characters; a tool server's max_result_chars wins; 0 = no cap).
	"tools.max_result_chars": raw(`100000`),
}

func raw(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("bad default: " + s)
	}
	b, _ := json.Marshal(v)
	return b
}
