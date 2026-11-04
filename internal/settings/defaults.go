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

	"auth.login_form":            raw(`false`),
	"auth.signup.oauth":          raw(`true`),
	"auth.signup.password":       raw(`false`),
	"auth.signup.default_role":   raw(`"user"`),
	"auth.jwt_expiry_hours":      raw(`720`),
	"auth.oauth.merge_by_email":  raw(`true`),
	"auth.oauth.update_picture":  raw(`true`),
	"auth.oauth.allowed_domains": raw(`[]`),

	// Default permissions for every non-admin user; groups can only add.
	"user.permissions": raw(`{
		"features": {"api_keys": false, "web_search": true, "code_interpreter": true, "memory": true, "temporary_chat": true},
		"chat": {"file_upload": true, "share": true, "export": true, "delete": true, "edit": true, "temporary": true},
		"workspace": {"models": false, "knowledge": false, "prompts": false, "tools": false}
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
	"tasks.autocomplete":           raw(`false`),
	"rag.embedding.connection":     raw(`""`),
	"rag.embedding.model":          raw(`""`),
	"rag.embedding.dim":            raw(`0`),
	"rag.embedding.batch":          raw(`32`),
	"rag.reranker.url":             raw(`""`),
	"rag.reranker.model":           raw(`""`),
	"rag.extractor.url":            raw(`""`),
	"rag.extractor.model":          raw(`""`),
	"rag.template":                 raw(`""`),
	"code_interpreter.engine":      raw(`""`),
	"code_interpreter.url":         raw(`""`),
	"code_interpreter.timeout":     raw(`60`),
	"code_interpreter.shared_dir":  raw(`""`),
	"code_interpreter.kernel_dir":  raw(`"/mnt/data"`),
	"code_interpreter.prompt":      raw(`""`),
	"code_interpreter.prelude":     raw(`""`),
	"code_interpreter.max_file_mb": raw(`50`),
	"spend.source.kind":            raw(`""`),
	"skills.dir":                   raw(`""`),
	"skills.grants":                raw(`[]`),
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
