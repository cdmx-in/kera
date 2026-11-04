package apply

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/cdmx-in/kera/internal/secret"
)

// Export reads every managed row back into a Config. Secrets are replaced by
// ${KERA_...} placeholders, returned so the caller can tell the user which
// variables an apply of the output needs.
func Export(ctx context.Context, db *pgxpool.Pool, keys *secret.Keys) (*Config, []string, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	r := &run{ctx: ctx, tx: tx, keys: keys}
	if err := r.loadGroups(); err != nil {
		return nil, nil, err
	}
	cfg := &Config{}
	var ph []string
	secretOf := func(s *string, parts ...string) {
		if *s != "" {
			*s = placeholder(parts...)
			ph = append(ph, *s)
		}
	}

	sets, err := managed(r.readSettings)
	if err != nil {
		return nil, nil, err
	}
	rows := map[string]setting{}
	for _, s := range sets {
		rows[s.id] = setting{JSON: s.v.(string)}
	}
	p, err := fillSettings(cfg, rows)
	if err != nil {
		return nil, nil, err
	}
	ph = append(ph, p...)

	groups, err := managed(r.readGroups)
	if err != nil {
		return nil, nil, err
	}
	for _, g := range groups {
		cfg.Groups = append(cfg.Groups, g.v.(Group))
	}

	provs, err := managed(r.readProviders)
	if err != nil {
		return nil, nil, err
	}
	for _, x := range provs {
		p := x.v.(OAuthProvider)
		if p.ID == "google" && p.Login {
			g := &GoogleLogin{ClientID: p.ClientID, ClientSecret: p.ClientSecret}
			secretOf(&g.ClientSecret, "google_client_secret")
			if err := fillGoogleSettings(g, rows); err != nil {
				return nil, nil, err
			}
			if cfg.Auth == nil {
				cfg.Auth = &Auth{}
			}
			cfg.Auth.OAuth = &AuthOAuth{Google: g}
			continue
		}
		secretOf(&p.ClientSecret, "oauth", p.ID, "client_secret")
		cfg.OAuthProviders = append(cfg.OAuthProviders, p)
	}

	conns, err := managed(r.readConnections)
	if err != nil {
		return nil, nil, err
	}
	for _, x := range conns {
		c := x.v.(Connection)
		secretOf(&c.APIKey, "connection", c.ID, "api_key")
		cfg.Connections = append(cfg.Connections, c)
	}

	tools, err := managed(r.readToolServers)
	if err != nil {
		return nil, nil, err
	}
	for _, x := range tools {
		t := x.v.(ToolServer)
		if t.Auth != nil {
			a := *t.Auth
			secretOf(&a.Key, "tool", t.ID, "key")
			t.Auth = &a
		}
		cfg.ToolServers = append(cfg.ToolServers, t)
	}

	hooks, err := managed(r.readHooks)
	if err != nil {
		return nil, nil, err
	}
	for _, x := range hooks {
		h := x.v.(HookServer)
		secretOf(&h.Key, "hook", h.ID, "key")
		cfg.HookServers = append(cfg.HookServers, h)
	}

	models, err := managed(r.readModels)
	if err != nil {
		return nil, nil, err
	}
	for _, x := range models {
		cfg.Models = append(cfg.Models, x.v.(Model))
	}

	know, err := managed(r.readKnowledgeGrants)
	if err != nil {
		return nil, nil, err
	}
	for _, x := range know {
		cfg.Knowledge = append(cfg.Knowledge, x.v.(KnowledgeGrant))
	}
	return cfg, ph, nil
}

func managed(read func() ([]row, error)) ([]row, error) {
	all, err := read()
	if err != nil {
		return nil, err
	}
	var out []row
	for _, x := range all {
		if x.managed {
			out = append(out, x)
		}
	}
	return out, nil
}

// Marshal renders cfg as YAML ($ in values is escaped as $$, except the
// ${KERA_...} placeholders export writes for secrets).
func Marshal(cfg *Config) ([]byte, error) {
	var node yaml.Node
	if err := node.Encode(cfg); err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	escapeDollars(&node)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var placeholderRe = regexp.MustCompile(`^\$\{KERA_[A-Z0-9_]+\}$`)

func escapeDollars(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && !placeholderRe.MatchString(n.Value) {
		n.Value = strings.ReplaceAll(n.Value, "$", "$$")
	}
	for _, c := range n.Content {
		escapeDollars(c)
	}
}
