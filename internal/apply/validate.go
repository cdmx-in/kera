package apply

import (
	"fmt"
	"slices"

	"github.com/cdmx-in/kera/internal/access"
)

// validate checks what can be checked without the database.
func validate(f *File, errs *Errors) {
	checkPerms("permissions", 0, f.Permissions, errs)
	unique(f, "groups", len(f.Groups), func(i int) string { return f.Groups[i].Name }, errs)
	for i, g := range f.Groups {
		checkPerms("groups["+g.Name+"].permissions", f.line("groups", i), g.Permissions, errs)
	}

	unique(f, "connections", len(f.Connections), func(i int) string { return f.Connections[i].ID }, errs)
	for i, c := range f.Connections {
		l := f.line("connections", i)
		oneOf(l, "connections["+c.ID+"].kind", c.Kind, errs, "ollama", "openai", "responses")
		if c.URL == "" {
			errs.add(l, "connections[%s]: url is required", c.ID)
		}
	}

	providers := map[string]bool{}
	unique(f, "oauth_providers", len(f.OAuthProviders), func(i int) string { return f.OAuthProviders[i].ID }, errs)
	for i, p := range f.OAuthProviders {
		providers[p.ID] = true
		if p.ClientID == "" {
			errs.add(f.line("oauth_providers", i), "oauth_providers[%s]: client_id is required", p.ID)
		}
	}
	if g := f.google(); g != nil {
		if providers["google"] {
			errs.add(0, "auth.oauth.google and oauth_providers[google] both define the provider \"google\"")
		}
		if g.ClientID == "" || g.ClientSecret == "" {
			errs.add(0, "auth.oauth.google: client_id and client_secret are required")
		}
	}

	unique(f, "tool_servers", len(f.ToolServers), func(i int) string { return f.ToolServers[i].ID }, errs)
	for i, t := range f.ToolServers {
		l, what := f.line("tool_servers", i), "tool_servers["+t.ID+"]"
		oneOf(l, what+".kind", t.Kind, errs, "openapi", "mcp")
		if t.URL == "" {
			errs.add(l, "%s: url is required", what)
		}
		if a := t.Auth; a != nil {
			oneOf(l, what+".auth.type", a.Type, errs, "none", "bearer", "header", "user_oauth")
			if (a.Type == "bearer" || a.Type == "header") && a.Key == "" {
				errs.add(l, "%s: auth.key is required for %s auth", what, a.Type)
			}
			if a.Type == "header" && a.Header == "" {
				errs.add(l, "%s: auth.header is required for header auth", what)
			}
			if a.Type == "user_oauth" && a.Provider == "" {
				errs.add(l, "%s: auth.provider is required for user_oauth auth", what)
			}
		}
		checkGrants(l, what, t.Grants, errs)
	}

	unique(f, "hook_servers", len(f.HookServers), func(i int) string { return f.HookServers[i].ID }, errs)
	for i, h := range f.HookServers {
		l, what := f.line("hook_servers", i), "hook_servers["+h.ID+"]"
		if h.URL == "" {
			errs.add(l, "%s: url is required", what)
		}
		for _, e := range h.Events {
			oneOf(l, what+".events", e, errs, "inlet", "outlet")
		}
		if h.Scope != "" {
			oneOf(l, what+".scope", h.Scope, errs, "global", "models")
		}
		if h.OnError != "" {
			oneOf(l, what+".on_error", h.OnError, errs, "skip", "abort")
		}
	}

	unique(f, "models", len(f.Models), func(i int) string { return f.Models[i].ID }, errs)
	for i, m := range f.Models {
		l, what := f.line("models", i), "models["+m.ID+"]"
		if m.Base != "" && m.Base == m.ID {
			errs.add(l, "%s: base cannot be the model itself", what)
		}
		if m.FileContext != "" {
			oneOf(l, what+".file_context", m.FileContext, errs, "tools", "full")
		}
		if m.Features != nil && m.Features.WebSearch != "" {
			oneOf(l, what+".features.web_search", m.Features.WebSearch, errs, "native", "builtin", "false")
		}
		for _, k := range m.Knowledge {
			if k.Name == "" && k.ID == "" {
				errs.add(l, "%s: knowledge entries need a name or an id", what)
			}
			if k.Mode != "" {
				oneOf(l, what+".knowledge.mode", k.Mode, errs, "retrieve", "full")
			}
		}
		checkGrants(l, what, m.Grants, errs)
	}

	unique(f, "knowledge", len(f.Knowledge), func(i int) string { return f.Knowledge[i].Name }, errs)
	for i, k := range f.Knowledge {
		checkGrants(f.line("knowledge", i), "knowledge["+k.Name+"]", k.Grants, errs)
	}
	for i, it := range f.Integrations {
		if it.ID == "" || it.Kind == "" {
			errs.add(f.line("integrations", i), "integrations: id and kind are required")
		}
	}
}

func (c *Config) google() *GoogleLogin {
	if c.Auth == nil || c.Auth.OAuth == nil {
		return nil
	}
	return c.Auth.OAuth.Google
}

func unique(f *File, section string, n int, id func(int) string, errs *Errors) {
	seen := map[string]bool{}
	for i := range n {
		k := id(i)
		switch {
		case k == "":
			errs.add(f.line(section, i), "%s: entry without an id/name", section)
		case seen[k]:
			errs.add(f.line(section, i), "%s: duplicate %q", section, k)
		}
		seen[k] = true
	}
}

func oneOf(line int, what, v string, errs *Errors, allowed ...string) {
	if !slices.Contains(allowed, v) {
		errs.add(line, "%s: %q is not one of %v", what, v, allowed)
	}
}

func checkGrants(line int, what string, gs []access.Grant, errs *Errors) {
	for _, g := range gs {
		if _, _, err := access.ParsePrincipal(g.Principal); err != nil {
			errs.add(line, "%s.grants: %v", what, err)
		}
		if g.Permission != "" {
			oneOf(line, what+".grants.permission", g.Permission, errs, access.Read, access.Write)
		}
	}
}

func checkPerms(what string, line int, p Permissions, errs *Errors) {
	known := defaultPerms()
	for cat, leaves := range p {
		for k := range leaves {
			if _, ok := known[cat][k]; !ok {
				errs.add(line, "%s: unknown permission %s", what, fmt.Sprintf("%s.%s", cat, k))
			}
		}
	}
}
