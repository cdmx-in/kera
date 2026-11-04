package models

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
)

// row is one model table row.
type row struct {
	ID              string
	ConnectionID    *string
	BaseModelID     *string
	OwnerID         *string
	Name            string
	Description     string
	SystemPrompt    string
	Params          map[string]any
	Capabilities    map[string]any
	Features        map[string]any
	DefaultFeatures []string
	FileContext     string
	Tools           Tools
	Knowledge       []KnowledgeRef
	Hooks           []string
	Suggestions     json.RawMessage
	Hidden          bool
	Active          bool
	Sort            int
	ImageURL        string
	Managed         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const rowColumns = `id, connection_id, base_model_id, owner_id, name, description, system_prompt, params, capabilities,
	features, default_features, file_context, tools, knowledge, hooks, suggestions, hidden, active, sort, image_url,
	managed, created_at, updated_at`

// loadRows reads every model row, or the one with id when id is non-empty.
func loadRows(ctx context.Context, db *pgxpool.Pool, id string) ([]row, error) {
	q, args := `SELECT `+rowColumns+` FROM model ORDER BY sort, id`, []any{}
	if id != "" {
		q, args = `SELECT `+rowColumns+` FROM model WHERE id = $1`, []any{id}
	}
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.ConnectionID, &r.BaseModelID, &r.OwnerID, &r.Name, &r.Description, &r.SystemPrompt,
			&r.Params, &r.Capabilities, &r.Features, &r.DefaultFeatures, &r.FileContext, &r.Tools, &r.Knowledge, &r.Hooks,
			&r.Suggestions, &r.Hidden, &r.Active, &r.Sort, &r.ImageURL, &r.Managed, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadRow returns one row or nil when absent.
func loadRow(ctx context.Context, db *pgxpool.Pool, id string) (*row, error) {
	rs, err := loadRows(ctx, db, id)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return &rs[0], nil
}

// saveRow inserts or replaces a row.
func saveRow(ctx context.Context, db *pgxpool.Pool, r *row) error {
	r.normalize()
	_, err := db.Exec(ctx, `INSERT INTO model (`+rowColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,now(),now())
		ON CONFLICT (id) DO UPDATE SET connection_id = EXCLUDED.connection_id, base_model_id = EXCLUDED.base_model_id,
		owner_id = EXCLUDED.owner_id, name = EXCLUDED.name, description = EXCLUDED.description,
		system_prompt = EXCLUDED.system_prompt, params = EXCLUDED.params, capabilities = EXCLUDED.capabilities,
		features = EXCLUDED.features, default_features = EXCLUDED.default_features, file_context = EXCLUDED.file_context,
		tools = EXCLUDED.tools, knowledge = EXCLUDED.knowledge, hooks = EXCLUDED.hooks, suggestions = EXCLUDED.suggestions,
		hidden = EXCLUDED.hidden, active = EXCLUDED.active, sort = EXCLUDED.sort, image_url = EXCLUDED.image_url,
		managed = EXCLUDED.managed, updated_at = now()`,
		r.ID, r.ConnectionID, r.BaseModelID, r.OwnerID, r.Name, r.Description, r.SystemPrompt, r.Params, r.Capabilities,
		r.Features, r.DefaultFeatures, r.FileContext, r.Tools, r.Knowledge, r.Hooks, r.Suggestions, r.Hidden, r.Active,
		r.Sort, r.ImageURL, r.Managed)
	if err != nil {
		return fmt.Errorf("save model %s: %w", r.ID, err)
	}
	return nil
}

// normalize replaces nils with empty values (the columns are NOT NULL).
func (r *row) normalize() {
	for _, m := range []*map[string]any{&r.Params, &r.Capabilities, &r.Features} {
		if *m == nil {
			*m = map[string]any{}
		}
	}
	for _, s := range []*[]string{&r.DefaultFeatures, &r.Hooks} {
		if *s == nil {
			*s = []string{}
		}
	}
	if r.Knowledge == nil {
		r.Knowledge = []KnowledgeRef{}
	}
	if len(r.Suggestions) == 0 || string(r.Suggestions) == "null" {
		r.Suggestions = json.RawMessage(`[]`)
	}
	if r.FileContext == "" {
		r.FileContext = "tools"
	}
}

// applyTo lays the row over m: params and capabilities merge (row wins), the
// system prompt replaces m's when non-empty, everything else is the row's.
func (r *row) applyTo(m *Resolved) {
	if r.Name != "" {
		m.Name = r.Name
	}
	if r.SystemPrompt != "" {
		m.SystemPrompt = r.SystemPrompt
	}
	m.Params = mergeMaps(m.Params, r.Params)
	m.Capabilities = mergeMaps(m.Capabilities, r.Capabilities)
	m.Description = r.Description
	m.Features = mergeMaps(nil, r.Features)
	m.DefaultFeatures = r.DefaultFeatures
	m.FileContext = r.FileContext
	m.Tools = r.Tools
	m.Knowledge = r.Knowledge
	m.Hooks = r.Hooks
	m.Suggestions = r.Suggestions
	m.Hidden = r.Hidden
	m.ImageURL = r.ImageURL
	m.Created = r.CreatedAt
	m.active = r.Active
	m.hasRow = true
}

// presetOf builds the preset r on top of parent: it inherits the connection,
// provider model, params, capabilities and system prompt.
func (r *row) presetOf(parent *Resolved) *Resolved {
	m := &Resolved{ID: r.ID, Name: r.ID, BaseModelID: *r.BaseModelID, Connection: parent.Connection,
		ProviderModel: parent.ProviderModel, Local: parent.Local, SystemPrompt: parent.SystemPrompt,
		Params: parent.Params, Capabilities: parent.Capabilities}
	r.applyTo(m)
	return m
}

func mergeMaps(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	maps.Copy(out, base)
	maps.Copy(out, over)
	return out
}

// rowFromModel starts a row for a discovered model that has none yet.
func rowFromModel(m *Resolved) *row {
	r := &row{ID: m.ID, Name: m.Name, Active: true}
	if !m.Connection.Implicit {
		id := m.Connection.ID
		r.ConnectionID = &id
	}
	r.normalize()
	return r
}

// rowJSON is the Open WebUI-style model shape, with Kera's extra fields in meta.
// params.system carries the system prompt.
func rowJSON(r *row) map[string]any {
	r.normalize()
	params := mergeMaps(r.Params, nil)
	if r.SystemPrompt != "" {
		params["system"] = r.SystemPrompt
	}
	builtins := r.Tools.Builtins
	if builtins == nil {
		builtins = map[string]bool{}
	}
	return map[string]any{
		"id":            r.ID,
		"user_id":       r.OwnerID,
		"base_model_id": r.BaseModelID,
		"connection_id": r.ConnectionID,
		"name":          r.Name,
		"params":        params,
		"meta": map[string]any{
			"description":        r.Description,
			"profile_image_url":  r.ImageURL,
			"toolIds":            r.Tools.IDs(),
			"builtinTools":       builtins,
			"disabledTools":      orEmpty(r.Tools.Disabled),
			"knowledge":          r.Knowledge,
			"capabilities":       r.Capabilities,
			"suggestion_prompts": r.Suggestions,
			"hidden":             r.Hidden,
			"features":           r.Features,
			"defaultFeatures":    r.DefaultFeatures,
			"fileContext":        r.FileContext,
			"hooks":              r.Hooks,
			"sort":               r.Sort,
		},
		"is_active":  r.Active,
		"managed":    r.Managed,
		"created_at": r.CreatedAt.Unix(),
		"updated_at": r.UpdatedAt.Unix(),
	}
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// form is a create/update body. Absent fields are left unchanged, so a
// client may post back what GET returned with one field edited.
type form struct {
	ID           string         `json:"id"`
	Name         *string        `json:"name"`
	BaseModelID  *string        `json:"base_model_id"` // "" clears
	ConnectionID *string        `json:"connection_id"` // "" clears
	Params       map[string]any `json:"params"`        // replaces; "system" sets the system prompt
	Meta         *metaForm      `json:"meta"`
	IsActive     *bool          `json:"is_active"`
}

type metaForm struct {
	Description       *string         `json:"description"`
	ProfileImageURL   *string         `json:"profile_image_url"`
	ToolIDs           *[]string       `json:"toolIds"`
	BuiltinTools      map[string]bool `json:"builtinTools"`
	DisabledTools     *[]string       `json:"disabledTools"`
	Knowledge         *[]KnowledgeRef `json:"knowledge"`
	Capabilities      map[string]any  `json:"capabilities"`
	SuggestionPrompts json.RawMessage `json:"suggestion_prompts"`
	Hidden            *bool           `json:"hidden"`
	Features          map[string]any  `json:"features"`
	DefaultFeatures   *[]string       `json:"defaultFeatures"`
	FileContext       *string         `json:"fileContext"`
	Hooks             *[]string       `json:"hooks"`
	Sort              *int            `json:"sort"`
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func optional(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// apply copies the fields present in f onto r.
func (f *form) apply(r *row) error {
	set(&r.Name, f.Name)
	if f.BaseModelID != nil {
		r.BaseModelID = optional(f.BaseModelID)
	}
	if f.ConnectionID != nil {
		r.ConnectionID = optional(f.ConnectionID)
	}
	if f.Params != nil {
		if s, ok := f.Params["system"].(string); ok {
			r.SystemPrompt = s
		}
		delete(f.Params, "system")
		r.Params = f.Params
	}
	set(&r.Active, f.IsActive)
	if m := f.Meta; m != nil {
		set(&r.Description, m.Description)
		set(&r.ImageURL, m.ProfileImageURL)
		set(&r.Hidden, m.Hidden)
		set(&r.DefaultFeatures, m.DefaultFeatures)
		set(&r.FileContext, m.FileContext)
		set(&r.Hooks, m.Hooks)
		set(&r.Sort, m.Sort)
		set(&r.Knowledge, m.Knowledge)
		set(&r.Tools.Disabled, m.DisabledTools)
		if m.Capabilities != nil {
			r.Capabilities = m.Capabilities
		}
		if m.Features != nil {
			r.Features = m.Features
		}
		if len(m.SuggestionPrompts) > 0 {
			r.Suggestions = m.SuggestionPrompts
		}
		if m.BuiltinTools != nil {
			r.Tools.Builtins = m.BuiltinTools
		}
		if m.ToolIDs != nil {
			r.Tools.setIDs(*m.ToolIDs)
		}
	}
	if r.FileContext != "" && r.FileContext != "tools" && r.FileContext != "full" {
		return api.Errf(http.StatusBadRequest, "bad_file_context", "meta.fileContext must be tools or full")
	}
	for _, k := range r.Knowledge {
		if k.ID == "" {
			return api.Errf(http.StatusBadRequest, "bad_knowledge", "meta.knowledge entries need an id")
		}
	}
	if r.BaseModelID != nil && *r.BaseModelID == r.ID {
		return api.Errf(http.StatusBadRequest, "bad_base_model", "a model cannot be its own base")
	}
	return nil
}

// setIDs applies a toolIds list: "builtin:<name>" entries enable builtins
// (enabled builtins missing from the list are dropped; explicit false
// entries stay), everything else is a tool server id ("server:" optional).
func (t *Tools) setIDs(toolIDs []string) {
	builtins := map[string]bool{}
	for k, v := range t.Builtins {
		if !v {
			builtins[k] = false
		}
	}
	t.Servers = nil
	for _, id := range toolIDs {
		if name, ok := strings.CutPrefix(id, "builtin:"); ok {
			builtins[name] = true
		} else {
			t.Servers = append(t.Servers, strings.TrimPrefix(id, "server:"))
		}
	}
	t.Builtins = builtins
}
