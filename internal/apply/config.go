// Package apply reads a declarative YAML file (site settings, connections,
// models, tool and hook servers, groups, grants) and makes the database match
// it in one transaction; Export prints the managed state back as YAML.
//
// Every row apply writes is marked managed. Rows created in the UI are left
// alone unless the file names them (then apply adopts them), and --prune only
// removes managed rows of the kinds the file declares.
package apply

import "github.com/cdmx-in/kera/internal/access"

// Config is the whole file. A nil section is "not declared": apply leaves the
// matching rows alone and --prune never touches them.
type Config struct {
	Site            *Site            `yaml:"site,omitempty"`
	Brand           *Brand           `yaml:"brand,omitempty"`
	Auth            *Auth            `yaml:"auth,omitempty"`
	Permissions     Permissions      `yaml:"permissions,omitempty"`
	Groups          []Group          `yaml:"groups,omitempty"`
	Connections     []Connection     `yaml:"connections,omitempty"`
	Tasks           *Tasks           `yaml:"tasks,omitempty"`
	Chat            *Chat            `yaml:"chat,omitempty"`
	RAG             *RAG             `yaml:"rag,omitempty"`
	Web             *Web             `yaml:"web,omitempty"`
	CodeInterpreter *CodeInterpreter `yaml:"code_interpreter,omitempty"`
	Cowork          *Cowork          `yaml:"cowork,omitempty"`
	Desktop         *Desktop         `yaml:"desktop,omitempty"`
	OAuthProviders  []OAuthProvider  `yaml:"oauth_providers,omitempty"`
	ToolServers     []ToolServer     `yaml:"tool_servers,omitempty"`
	HookServers     []HookServer     `yaml:"hook_servers,omitempty"`
	Models          []Model          `yaml:"models,omitempty"`
	Knowledge       []KnowledgeGrant `yaml:"knowledge,omitempty"`
	Suggestions     []Suggestion     `yaml:"suggestions,omitempty"`
	Integrations    []Integration    `yaml:"integrations,omitempty"`
	Spend           *Spend           `yaml:"spend,omitempty"`
	Compat          *Compat          `yaml:"compat,omitempty"`
	Guardrails      *Guardrails      `yaml:"guardrails,omitempty"`
	Skills          *Skills          `yaml:"skills,omitempty"`
}

// Settings sections. Every leaf is a pointer, slice or map so "absent" differs
// from "zero"; only present leaves become config rows. A leaf maps to the key
// <section>.<path> unless it carries a key tag.

type Site struct {
	Name             *string  `yaml:"name,omitempty"`
	URL              *string  `yaml:"url,omitempty"`
	Timezone         *string  `yaml:"timezone,omitempty"`
	DefaultModels    []string `yaml:"default_models,omitempty"`
	ModelOrder       []string `yaml:"model_order,omitempty"`
	BaseModelsPublic *bool    `yaml:"base_models_public,omitempty" key:"models.base_public"`
}

// Brand is the frontend's brand object (stored whole as ui.brand); ToolDisplay
// is stored separately as ui.tool_display.
type Brand struct {
	Name           string                 `yaml:"name,omitempty" json:"name,omitempty"`
	Tagline        string                 `yaml:"tagline,omitempty" json:"tagline,omitempty"`
	Logo           *ThemedImage           `yaml:"logo,omitempty" json:"logo,omitempty"`
	Mark           string                 `yaml:"mark,omitempty" json:"mark,omitempty"` // small square logo (sidebar, login card); falls back to favicon
	Favicon        string                 `yaml:"favicon,omitempty" json:"favicon,omitempty"`
	FaviconAlt     []string               `yaml:"favicon_alt,omitempty" json:"faviconAlt,omitempty"` // other formats of the favicon (png, ico) for older browsers
	AppleTouchIcon string                 `yaml:"apple_touch_icon,omitempty" json:"appleTouchIcon,omitempty"`
	PWAIcons       []string               `yaml:"pwa_icons,omitempty" json:"pwaIcons,omitempty"`
	Splash         *Splash                `yaml:"splash,omitempty" json:"splash,omitempty"`
	Accent         *Accent                `yaml:"accent,omitempty" json:"accent,omitempty"`
	Grey           *Grey                  `yaml:"grey,omitempty" json:"grey,omitempty"`
	Background     string                 `yaml:"background,omitempty" json:"background,omitempty"`
	Login          *Login                 `yaml:"login,omitempty" json:"login,omitempty"`
	DefaultTheme   string                 `yaml:"default_theme,omitempty" json:"defaultTheme,omitempty"`
	CustomCSS      string                 `yaml:"custom_css,omitempty" json:"customCss,omitempty"`
	ToolDisplay    map[string]ToolDisplay `yaml:"tool_display,omitempty" json:"-"`
}

type ThemedImage struct {
	Light string `yaml:"light,omitempty" json:"light,omitempty"`
	Dark  string `yaml:"dark,omitempty" json:"dark,omitempty"`
}

type Splash struct {
	Logo     string `yaml:"logo,omitempty" json:"logo,omitempty"`
	LogoDark string `yaml:"logo_dark,omitempty" json:"logoDark,omitempty"` // dark-mode logo; may sit on its own dark tile (blended with lighten)
	Wordmark string `yaml:"wordmark,omitempty" json:"wordmark,omitempty"`
	Style    string `yaml:"style,omitempty" json:"style,omitempty"`
}

type Accent struct {
	Base   string `yaml:"base,omitempty" json:"base,omitempty"`
	Bright string `yaml:"bright,omitempty" json:"bright,omitempty"`
}

type Grey struct {
	Hue    float64  `yaml:"hue,omitempty" json:"hue,omitempty"`
	Chroma float64  `yaml:"chroma,omitempty" json:"chroma,omitempty"`
	Scale  []string `yaml:"scale,omitempty" json:"scale,omitempty"`
}

type Login struct {
	Wordmark string `yaml:"wordmark,omitempty" json:"wordmark,omitempty"`
	Style    string `yaml:"style,omitempty" json:"style,omitempty"`
}

// ToolDisplay is how the UI names one tool's steps ("web search", 2 "web searches").
type ToolDisplay struct {
	Label  string `yaml:"label,omitempty" json:"label,omitempty"`
	Icon   string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Kind   string `yaml:"kind,omitempty" json:"kind,omitempty"`
	Plural string `yaml:"plural,omitempty" json:"plural,omitempty"`
}

type Auth struct {
	OAuth          *AuthOAuth `yaml:"oauth,omitempty" apply:"-"`
	LoginForm      *bool      `yaml:"login_form,omitempty"`
	Signup         *Signup    `yaml:"signup,omitempty"`
	JWTExpiryHours *int       `yaml:"jwt_expiry_hours,omitempty"`
}

type AuthOAuth struct {
	Google *GoogleLogin `yaml:"google,omitempty"`
}

// GoogleLogin becomes the oauth_provider row "google" (options.login = true);
// the merge/picture/domain rules are settings under auth.oauth.*.
type GoogleLogin struct {
	ClientID       string   `yaml:"client_id"`
	ClientSecret   string   `yaml:"client_secret"`
	MergeByEmail   *bool    `yaml:"merge_by_email,omitempty"`
	UpdatePicture  *bool    `yaml:"update_picture,omitempty"`
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
}

type Signup struct {
	OAuth       *bool   `yaml:"oauth,omitempty"`
	Password    *bool   `yaml:"password,omitempty"`
	DefaultRole *string `yaml:"default_role,omitempty"`
}

// Permissions are the defaults for every non-admin user, e.g.
// {features: {api_keys: true}}. Keys must exist in the built-in defaults.
type Permissions map[string]map[string]bool

type Group struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description,omitempty"`
	Permissions Permissions `yaml:"permissions,omitempty"`
}

type Connection struct {
	ID                 string            `yaml:"id"`
	Kind               string            `yaml:"kind"`
	URL                string            `yaml:"url"`
	APIKey             string            `yaml:"api_key,omitempty"`
	Prefix             string            `yaml:"prefix,omitempty"`
	Models             []string          `yaml:"models,omitempty"`
	Headers            map[string]string `yaml:"headers,omitempty"`
	WebSearch          *WebSearchOpts    `yaml:"web_search,omitempty"`
	ForwardUserHeaders *ForwardHeaders   `yaml:"forward_user_headers,omitempty"`
	Enabled            *bool             `yaml:"enabled,omitempty"`
}

// WebSearchOpts tunes a Responses connection's native web_search tool.
type WebSearchOpts struct {
	Country     string `yaml:"country,omitempty" json:"country,omitempty"`
	City        string `yaml:"city,omitempty" json:"city,omitempty"`
	Timezone    string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	ContextSize string `yaml:"context_size,omitempty" json:"context_size,omitempty"`
}

// ForwardHeaders names the request headers that carry the chatting user's
// identity upstream (e.g. for LiteLLM spend attribution). Empty = not sent.
type ForwardHeaders struct {
	Email string `yaml:"email,omitempty" json:"email,omitempty"`
	ID    string `yaml:"id,omitempty" json:"id,omitempty"`
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Role  string `yaml:"role,omitempty" json:"role,omitempty"`
}

type Tasks struct {
	ModelExternal *string `yaml:"model_external,omitempty"`
	ModelLocal    *string `yaml:"model_local,omitempty"`
	Title         *bool   `yaml:"title,omitempty"`
	Tags          *bool   `yaml:"tags,omitempty"`
	FollowUps     *bool   `yaml:"follow_ups,omitempty"`
	Query         *bool   `yaml:"query,omitempty"`
	Autocomplete  *bool   `yaml:"autocomplete,omitempty"`
}

type Chat struct {
	Compaction        *Compaction `yaml:"compaction,omitempty"`
	MaxToolIterations *int        `yaml:"max_tool_iterations,omitempty"`
	LocalLock         *bool       `yaml:"local_lock,omitempty"`
	Memory            *Memory     `yaml:"memory,omitempty"`
}

type Compaction struct {
	Enable    *bool `yaml:"enable,omitempty"`
	Threshold *int  `yaml:"threshold,omitempty"`
	Retention *int  `yaml:"retention,omitempty"`
}

type Memory struct {
	Enable      *bool `yaml:"enable,omitempty"`
	ReviewEvery *int  `yaml:"review_every,omitempty"`
	LocalOnly   *bool `yaml:"local_only,omitempty"`
}

type RAG struct {
	ChunkSize           *int       `yaml:"chunk_size,omitempty"`
	ChunkOverlap        *int       `yaml:"chunk_overlap,omitempty"`
	MarkdownHeaders     *bool      `yaml:"markdown_headers,omitempty"`
	Embedding           *Embedding `yaml:"embedding,omitempty"`
	Hybrid              *Hybrid    `yaml:"hybrid,omitempty"`
	TopK                *int       `yaml:"top_k,omitempty"`
	TopKRerank          *int       `yaml:"top_k_rerank,omitempty"`
	RelevanceThreshold  *float64   `yaml:"relevance_threshold,omitempty"`
	Reranker            *Reranker  `yaml:"reranker,omitempty"`
	Extractor           *Endpoint  `yaml:"extractor,omitempty"`
	Template            *string    `yaml:"template,omitempty"`
	TemplateFile        string     `yaml:"template_file,omitempty" apply:"-"`
	FullContextMaxChars *int       `yaml:"full_context_max_chars,omitempty"`
}

type Embedding struct {
	Connection *string `yaml:"connection,omitempty"`
	Model      *string `yaml:"model,omitempty"`
	Dim        *int    `yaml:"dim,omitempty"`
	Batch      *int    `yaml:"batch,omitempty"`
}

type Hybrid struct {
	BM25Weight *float64 `yaml:"bm25_weight,omitempty"`
}

type Endpoint struct {
	URL   *string `yaml:"url,omitempty"`
	Model *string `yaml:"model,omitempty"`
}

// Reranker is a Cohere-style POST /rerank endpoint (sidecar, TEI, LiteLLM).
type Reranker struct {
	URL    *string `yaml:"url,omitempty"`
	Model  *string `yaml:"model,omitempty"`
	APIKey *string `yaml:"api_key,omitempty" secret:"true"` // sent as Authorization: Bearer
	// LocalRuns decides what runs on local models do with an external
	// reranker: skip (default: keep the fused order, or use LocalURL) or allow.
	LocalRuns *string `yaml:"local_runs,omitempty"`
	LocalURL  *string `yaml:"local_url,omitempty"` // reranker for local runs (same model, no key)
}

type Web struct {
	Search *WebSearch `yaml:"search,omitempty"`
	Fetch  *WebFetch  `yaml:"fetch,omitempty"`
}

type WebSearch struct {
	Engine          *string `yaml:"engine,omitempty"`
	ResultCount     *int    `yaml:"result_count,omitempty"`
	MaxCallsPerTurn *int    `yaml:"max_calls_per_turn,omitempty"`
	URL             *string `yaml:"url,omitempty"`                   // searxng base URL
	APIKey          *string `yaml:"api_key,omitempty" secret:"true"` // brave subscription token
}

type WebFetch struct {
	MaxChars        *int  `yaml:"max_chars,omitempty"`
	MaxCallsPerTurn *int  `yaml:"max_calls_per_turn,omitempty"`
	AllowPrivate    *bool `yaml:"allow_private,omitempty"` // let fetch_url reach private networks
}

type CodeInterpreter struct {
	Engine      *string `yaml:"engine,omitempty"`
	URL         *string `yaml:"url,omitempty"`
	Token       *string `yaml:"token,omitempty" secret:"true"`
	Timeout     *int    `yaml:"timeout,omitempty"`
	SharedDir   *string `yaml:"shared_dir,omitempty"`
	KernelDir   *string `yaml:"kernel_dir,omitempty"`
	Prompt      *string `yaml:"prompt,omitempty"`
	PromptFile  string  `yaml:"prompt_file,omitempty" apply:"-"`
	Prelude     *string `yaml:"prelude,omitempty"`
	PreludeFile string  `yaml:"prelude_file,omitempty" apply:"-"`
	MaxFileMB   *int    `yaml:"max_file_mb,omitempty"`
	// Languages replaces the whole code_interpreter.languages map.
	Languages map[string]CodeLanguage `yaml:"languages,omitempty"`
}

// Cowork is task mode: the prompt added to task runs ("" = built-in) and their tool-round cap.
type Cowork struct {
	Prompt            *string `yaml:"prompt,omitempty"`
	PromptFile        string  `yaml:"prompt_file,omitempty" apply:"-"`
	MaxToolIterations *int    `yaml:"max_tool_iterations,omitempty"`
}

// Desktop is the desktop sidecar's live view: its API URL, websockify URL and service key.
type Desktop struct {
	URL     *string `yaml:"url,omitempty"`
	ViewURL *string `yaml:"view_url,omitempty"`
	Key     *string `yaml:"key,omitempty" secret:"true"`
}

// CodeLanguage is one language execute_code can run. An empty Command means
// the Jupyter kernel itself (Python); otherwise Command is an argv (never a
// shell string) whose "{file}" is replaced by the script path.
type CodeLanguage struct {
	Command  []string `yaml:"command,omitempty" json:"command,omitempty"`
	Ext      string   `yaml:"ext,omitempty" json:"ext,omitempty"`
	Aliases  []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Label    string   `yaml:"label,omitempty" json:"label,omitempty"`
	Check    []string `yaml:"check,omitempty" json:"check,omitempty"`       // must exit 0 for Command to count as installed
	Fallback []string `yaml:"fallback,omitempty" json:"fallback,omitempty"` // used when Command is not installed
}

type OAuthProvider struct {
	ID                    string `yaml:"id"`
	Issuer                string `yaml:"issuer,omitempty"`
	AuthorizationEndpoint string `yaml:"authorization_endpoint,omitempty"`
	TokenEndpoint         string `yaml:"token_endpoint,omitempty"`
	RevocationEndpoint    string `yaml:"revocation_endpoint,omitempty"`
	UserinfoEndpoint      string `yaml:"userinfo_endpoint,omitempty"`
	ClientID              string `yaml:"client_id"`
	ClientSecret          string `yaml:"client_secret,omitempty"`
	Scope                 string `yaml:"scope,omitempty"`
	PKCE                  *bool  `yaml:"pkce,omitempty"`
	SendResource          bool   `yaml:"send_resource,omitempty"`
	Name                  string `yaml:"name,omitempty"`
	Login                 bool   `yaml:"login,omitempty"` // offered on the sign-in page
}

type ToolServer struct {
	ID                      string             `yaml:"id"`
	Kind                    string             `yaml:"kind"`
	Name                    string             `yaml:"name,omitempty"`
	Description             string             `yaml:"description,omitempty"`
	URL                     string             `yaml:"url"`
	Spec                    string             `yaml:"spec,omitempty"`
	Auth                    *ToolAuth          `yaml:"auth,omitempty"`
	Grants                  []access.Grant     `yaml:"grants,omitempty"`
	Filter                  []string           `yaml:"filter,omitempty"`
	Disable                 []string           `yaml:"disable,omitempty"`
	MaxResultChars          int                `yaml:"max_result_chars,omitempty"`
	AutoEnableWhenConnected bool               `yaml:"auto_enable_when_connected,omitempty"`
	Display                 *ToolServerDisplay `yaml:"display,omitempty"`
	Enabled                 *bool              `yaml:"enabled,omitempty"`
}

// ToolAuth: none | bearer (key) | header (header + key) | user_oauth (provider,
// plus an optional header + key sent next to the user's token).
// The key is stored encrypted inside tool_server.auth as key_enc (base64).
type ToolAuth struct {
	Type     string `yaml:"type"`
	Key      string `yaml:"key,omitempty"`
	Header   string `yaml:"header,omitempty"`
	Provider string `yaml:"provider,omitempty"`
}

// ToolServerDisplay names the server and, per function, its steps in the UI.
type ToolServerDisplay struct {
	Label  string                 `yaml:"label,omitempty" json:"label,omitempty"`
	Icon   string                 `yaml:"icon,omitempty" json:"icon,omitempty"`
	Kind   string                 `yaml:"kind,omitempty" json:"kind,omitempty"`
	Plural string                 `yaml:"plural,omitempty" json:"plural,omitempty"`
	Tools  map[string]ToolDisplay `yaml:"tools,omitempty" json:"tools,omitempty"`
}

type HookServer struct {
	ID           string         `yaml:"id"`
	Name         string         `yaml:"name,omitempty"`
	URL          string         `yaml:"url"`
	Key          string         `yaml:"key,omitempty"`
	Events       []string       `yaml:"events,omitempty"`
	Scope        string         `yaml:"scope,omitempty"`
	ModelIDs     []string       `yaml:"model_ids,omitempty"`
	Priority     int            `yaml:"priority,omitempty"`
	Config       map[string]any `yaml:"config,omitempty"`
	ForwardOAuth []string       `yaml:"forward_oauth,omitempty"`
	TimeoutMS    int            `yaml:"timeout_ms,omitempty"`
	OnError      string         `yaml:"on_error,omitempty"`
	Enabled      *bool          `yaml:"enabled,omitempty"`
}

// Model is a preset (base set) or an override of a discovered base model
// (base empty; connection optionally pins it to one connection).
type Model struct {
	ID              string         `yaml:"id"`
	Name            string         `yaml:"name,omitempty"`
	Base            string         `yaml:"base,omitempty"`
	Connection      string         `yaml:"connection,omitempty"`
	Description     string         `yaml:"description,omitempty"`
	Image           string         `yaml:"image,omitempty"`
	Prompt          string         `yaml:"prompt,omitempty"`
	PromptFile      string         `yaml:"prompt_file,omitempty"`
	Params          map[string]any `yaml:"params,omitempty"`
	Capabilities    map[string]any `yaml:"capabilities,omitempty"`
	Features        *ModelFeatures `yaml:"features,omitempty"`
	DefaultFeatures []string       `yaml:"default_features,omitempty"`
	FileContext     string         `yaml:"file_context,omitempty"`
	Tools           *ModelTools    `yaml:"tools,omitempty"`
	Knowledge       []KnowledgeRef `yaml:"knowledge,omitempty"`
	Hooks           []string       `yaml:"hooks,omitempty"`
	Suggestions     []Suggestion   `yaml:"suggestions,omitempty"`
	Hidden          bool           `yaml:"hidden,omitempty"`
	Active          *bool          `yaml:"active,omitempty"`
	Grants          []access.Grant `yaml:"grants,omitempty"`
}

// ModelFeatures: web_search is native (provider tool), builtin (Kera's web
// tools) or false.
type ModelFeatures struct {
	WebSearch       string `yaml:"web_search,omitempty" json:"web_search,omitempty"`
	CodeInterpreter *bool  `yaml:"code_interpreter,omitempty" json:"code_interpreter,omitempty"`
	Memory          *bool  `yaml:"memory,omitempty" json:"memory,omitempty"`
	// Guardrails overrides the guardrails.* settings for this model (keys pii,
	// output_pii, injection, moderation, local_exempt, blocklist).
	Guardrails map[string]any `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
}

type ModelTools struct {
	Servers  []string        `yaml:"servers,omitempty" json:"servers,omitempty"`
	Builtins map[string]bool `yaml:"builtins,omitempty" json:"builtins,omitempty"`
}

// KnowledgeRef points at a collection by name (resolved at run time, so the
// collection may be created after apply) or by id. Mode is auto (unset:
// retrieve when a working reranker serves the run, else tools when the model
// has tool servers, else retrieve), retrieve (passages in the prompt), full
// (whole documents in the prompt) or tools (the model searches with the
// knowledge tools; nothing is injected).
type KnowledgeRef struct {
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	ID   string `yaml:"id,omitempty" json:"id,omitempty"`
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// KnowledgeGrant sets who may use a collection, by collection name. The
// collections themselves are created by uploads, not by apply.
type KnowledgeGrant struct {
	Name   string         `yaml:"name"`
	Grants []access.Grant `yaml:"grants"`
}

type Suggestion struct {
	Title   []string `yaml:"title,omitempty,flow" json:"title,omitempty"`
	Content string   `yaml:"content" json:"content"`
}

// Integration is one frontend integration (stored in ui.integrations).
type Integration struct {
	ID             string            `yaml:"id" json:"id"`
	Kind           string            `yaml:"kind" json:"kind"`
	Label          string            `yaml:"label,omitempty" json:"label,omitempty"`
	Icon           string            `yaml:"icon,omitempty" json:"icon,omitempty"`
	ToolServerID   string            `yaml:"tool_server_id,omitempty" json:"toolServerId,omitempty"`
	AutoEnable     bool              `yaml:"auto_enable,omitempty" json:"autoEnable,omitempty"`
	ShowInPlusMenu bool              `yaml:"show_in_plus_menu,omitempty" json:"showInPlusMenu,omitempty"`
	Version        string            `yaml:"version,omitempty" json:"version,omitempty"`
	DownloadURL    string            `yaml:"download_url,omitempty" json:"downloadUrl,omitempty"`
	MarkerAttr     string            `yaml:"marker_attr,omitempty" json:"markerAttr,omitempty"`
	Platforms      []string          `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Steps          map[string]string `yaml:"steps,omitempty" json:"steps,omitempty"`
	Commands       map[string]string `yaml:"commands,omitempty" json:"commands,omitempty"`
}

type Spend struct {
	Source *SpendSource `yaml:"source,omitempty"`
}

type SpendSource struct {
	Kind *string `yaml:"kind,omitempty"`
	DSN  *string `yaml:"dsn,omitempty" secret:"true"`
}

type Compat struct {
	ThrowawayChats *bool `yaml:"throwaway_chats,omitempty"`
}

// Guardrails: checks on every model call. Modes are off, log, redact (pii
// and output_pii only) or block (not output_pii). Blocklist regexes always
// block. Models override leaves with features.guardrails.
type Guardrails struct {
	PII         *string          `yaml:"pii,omitempty"`
	OutputPII   *string          `yaml:"output_pii,omitempty"`
	Injection   *string          `yaml:"injection,omitempty"`
	Blocklist   []string         `yaml:"blocklist,omitempty"`
	Moderation  *GuardModeration `yaml:"moderation,omitempty"`
	LocalExempt *bool            `yaml:"local_exempt,omitempty"`
}

type GuardModeration struct {
	Mode  *string `yaml:"mode,omitempty" key:"guardrails.moderation"`
	Model *string `yaml:"model,omitempty"`
}

// Skills imports every SKILL.md under Dir and in ImportZip (paths relative to
// the config file); Grants apply to each of them (none = admins only). Skills
// are matched by name; enabled/lazy set in the admin console are kept.
type Skills struct {
	Dir       string         `yaml:"dir,omitempty"`
	ImportZip string         `yaml:"import_zip,omitempty"`
	Grants    []access.Grant `yaml:"grants,omitempty"`
}
