package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
)

// Options control one apply.
type Options struct {
	DryRun bool // compute and print the plan, write nothing
	Prune  bool // delete managed rows of declared kinds that the file no longer lists
}

// Actions in a plan.
const (
	Create    = "create"
	Update    = "update"
	Unchanged = "unchanged"
	Delete    = "delete"
)

// Change is one line of the plan.
type Change struct {
	Kind   string   // setting, group, oauth_provider, connection, tool_server, hook_server, model, knowledge
	ID     string   // key, name or id
	Action string   // create, update, unchanged, delete
	Fields []string // changed fields of an update (names only, never values)
}

// Result is the plan (and, without --dry-run, what was done).
type Result struct {
	Changes  []Change
	Warnings []string
	DryRun   bool
}

// Count returns how many changes have the action.
func (r *Result) Count(action string) int {
	n := 0
	for _, c := range r.Changes {
		if c.Action == action {
			n++
		}
	}
	return n
}

// Print writes the plan, one change per line, then a summary.
func (r *Result) Print(w io.Writer) {
	for _, c := range r.Changes {
		line := fmt.Sprintf("%-9s  %-14s %s", c.Action, c.Kind, c.ID)
		if len(c.Fields) > 0 {
			line += " (" + strings.Join(c.Fields, ", ") + ")"
		}
		fmt.Fprintln(w, line)
	}
	for _, m := range r.Warnings {
		fmt.Fprintln(w, "warning:", m)
	}
	tail := ""
	if r.DryRun {
		tail = " (dry run: nothing written)"
	}
	fmt.Fprintf(w, "%d to create, %d to update, %d unchanged, %d to delete%s\n",
		r.Count(Create), r.Count(Update), r.Count(Unchanged), r.Count(Delete), tail)
}

type run struct {
	ctx       context.Context
	tx        pgx.Tx
	keys      *secret.Keys
	f         *File
	opt       Options
	res       *Result
	errs      Errors
	groupID   map[string]string // name -> id
	groupName map[string]string // id -> name
}

type item struct {
	id   string
	v    any
	line int
	bad  bool // a reference did not resolve (already reported); not written
}

// Apply makes the database match f in one transaction. On validation or
// resolution errors nothing is written and the errors are returned together.
// st is invalidated after commit so the server sees new settings (pass nil
// when no Store is running in this process).
func Apply(ctx context.Context, db *pgxpool.Pool, keys *secret.Keys, st *settings.Store, f *File, opt Options) (*Result, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	r := &run{ctx: ctx, tx: tx, keys: keys, f: f, opt: opt, res: &Result{DryRun: opt.DryRun}}
	if err := r.all(); err != nil {
		return nil, err
	}
	if len(r.errs) > 0 {
		return r.res, r.errs
	}
	if opt.DryRun {
		return r.res, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if st != nil {
		st.Invalidate()
	}
	return r.res, nil
}

func (r *run) all() error {
	f := r.f
	desired, err := desiredSettings(&f.Config)
	if err != nil {
		return err
	}
	var sets []item
	for _, k := range sortedKeys(desired) {
		sets = append(sets, item{id: k, v: desired[k].JSON})
	}
	if err := r.reconcile("setting", len(sets) > 0, sets, r.readSettings,
		func(it item) error { return r.writeSetting(it.id, desired[it.id]) }, r.deleteSetting); err != nil {
		return err
	}

	var groups []item
	for i, g := range f.Groups {
		groups = append(groups, item{id: g.Name, v: normGroup(g), line: f.line("groups", i)})
	}
	if err := r.reconcile("group", f.Groups != nil, groups, r.readGroups,
		func(it item) error { return r.writeGroup(it.v.(Group)) }, r.deleteGroup); err != nil {
		return err
	}
	if err := r.loadGroups(); err != nil {
		return err
	}

	var provs []item
	if g := f.google(); g != nil {
		provs = append(provs, item{id: "google", v: normProvider(OAuthProvider{ID: "google", ClientID: g.ClientID,
			ClientSecret: g.ClientSecret, Login: true})})
	}
	for i, p := range f.OAuthProviders {
		provs = append(provs, item{id: p.ID, v: normProvider(p), line: f.line("oauth_providers", i)})
	}
	if err := r.reconcile("oauth_provider", f.OAuthProviders != nil || f.google() != nil, provs, r.readProviders,
		func(it item) error { return r.writeProvider(it.v.(OAuthProvider)) },
		func(id string) error { return r.exec("DELETE FROM oauth_provider WHERE id = $1 AND managed", id) }); err != nil {
		return err
	}

	var conns []item
	for i, c := range f.Connections {
		conns = append(conns, item{id: c.ID, v: normConnection(c), line: f.line("connections", i)})
	}
	if err := r.reconcile("connection", f.Connections != nil, conns, r.readConnections,
		func(it item) error { return r.writeConnection(it.v.(Connection)) },
		func(id string) error { return r.exec("DELETE FROM connection WHERE id = $1 AND managed", id) }); err != nil {
		return err
	}

	var tools []item
	for i, t := range f.ToolServers {
		l, n := f.line("tool_servers", i), len(r.errs)
		t.Grants = r.normGrants(l, "tool_servers["+t.ID+"]", t.Grants)
		if t.Auth != nil && t.Auth.Type == "user_oauth" {
			r.mustExist(l, "tool_servers["+t.ID+"].auth.provider", "oauth_provider", t.Auth.Provider)
		}
		tools = append(tools, item{t.ID, normToolServer(t), l, len(r.errs) > n})
	}
	if err := r.reconcile("tool_server", f.ToolServers != nil, tools, r.readToolServers,
		func(it item) error { return r.writeToolServer(it.v.(ToolServer)) },
		func(id string) error { return r.deleteWithGrants("tool_server", access.ToolServer, id) }); err != nil {
		return err
	}

	var hooks []item
	for i, h := range f.HookServers {
		hooks = append(hooks, item{id: h.ID, v: normHook(h), line: f.line("hook_servers", i)})
	}
	if err := r.reconcile("hook_server", f.HookServers != nil, hooks, r.readHooks,
		func(it item) error { return r.writeHook(it.v.(HookServer)) },
		func(id string) error { return r.exec("DELETE FROM hook_server WHERE id = $1 AND managed", id) }); err != nil {
		return err
	}

	var models []item
	for i, m := range f.Models {
		l, n := f.line("models", i), len(r.errs)
		m.Grants = r.normGrants(l, "models["+m.ID+"]", m.Grants)
		if m.Connection != "" {
			r.mustExist(l, "models["+m.ID+"].connection", "connection", m.Connection)
		}
		if m.Tools != nil {
			for _, s := range m.Tools.Servers {
				r.mustExist(l, "models["+m.ID+"].tools.servers", "tool_server", s)
			}
		}
		for _, h := range m.Hooks {
			r.mustExist(l, "models["+m.ID+"].hooks", "hook_server", h)
		}
		models = append(models, item{m.ID, normModel(m), l, len(r.errs) > n})
	}
	if err := r.reconcile("model", f.Models != nil, models, r.readModels,
		func(it item) error { return r.writeModel(it.v.(Model)) },
		func(id string) error { return r.deleteWithGrants("model", access.Model, id) }); err != nil {
		return err
	}

	return r.knowledge()
}

// knowledge sets grants on collections named in the file. A collection that
// does not exist yet is skipped with a warning (uploads create collections).
func (r *run) knowledge() error {
	kids, err := r.knowledgeIDs()
	if err != nil {
		return err
	}
	var items []item
	for i, k := range r.f.Knowledge {
		l := r.f.line("knowledge", i)
		switch ids := kids[k.Name]; {
		case len(ids) == 0:
			r.res.Warnings = append(r.res.Warnings, fmt.Sprintf("knowledge %q does not exist yet; its grants apply on a later run", k.Name))
			continue
		case len(ids) > 1:
			r.errs.add(l, "knowledge %q: %d collections have this name; rename one", k.Name, len(ids))
			continue
		}
		n := len(r.errs)
		g := r.normGrants(l, "knowledge["+k.Name+"]", k.Grants)
		items = append(items, item{k.Name, KnowledgeGrant{Name: k.Name, Grants: g}, l, len(r.errs) > n})
	}
	return r.reconcile("knowledge", r.f.Knowledge != nil, items, r.readKnowledgeGrants,
		func(it item) error {
			return r.setGrants(access.Knowledge, kids[it.id][0], it.v.(KnowledgeGrant).Grants)
		},
		func(name string) error {
			for _, id := range kids[name] {
				if err := r.exec("DELETE FROM access_grant WHERE resource_type = 'knowledge' AND resource_id = $1 AND managed", id); err != nil {
					return err
				}
			}
			return nil
		})
}

// reconcile compares desired items with current rows, records the plan and
// writes the differences. Rows that are not managed are adopted when the file
// names them and never deleted.
func (r *run) reconcile(kind string, declared bool, desired []item, read func() ([]row, error),
	write func(item) error, remove func(string) error) error {
	current, err := read()
	if err != nil {
		return err
	}
	cur := map[string]row{}
	for _, c := range current {
		cur[c.id] = c
	}
	want := map[string]bool{}
	for _, it := range desired {
		want[it.id] = true
		if it.bad {
			continue
		}
		ch := Change{Kind: kind, ID: it.id, Action: Create}
		if c, ok := cur[it.id]; ok {
			ch.Action, ch.Fields = Unchanged, diff(c.v, it.v)
			if len(ch.Fields) > 0 || !c.managed {
				ch.Action = Update
			}
			if len(ch.Fields) == 0 && !c.managed {
				ch.Fields = []string{"managed"}
			}
		}
		r.res.Changes = append(r.res.Changes, ch)
		if ch.Action == Unchanged {
			continue
		}
		if err := write(it); err != nil {
			return err
		}
	}
	if !r.opt.Prune || !declared {
		return nil
	}
	for _, c := range current {
		if !c.managed || want[c.id] {
			continue
		}
		switch err := remove(c.id); {
		case errors.Is(err, errKept):
		case err != nil:
			return err
		default:
			r.res.Changes = append(r.res.Changes, Change{Kind: kind, ID: c.id, Action: Delete})
		}
	}
	return nil
}

// diff lists the top-level fields that differ between two normalised values,
// compared through their YAML form (so nil and empty, int and float agree).
func diff(cur, want any) []string {
	a, b := yamlMap(cur), yamlMap(want)
	if a == nil || b == nil {
		if reflect.DeepEqual(yamlOf(cur), yamlOf(want)) {
			return nil
		}
		return []string{"value"}
	}
	var out []string
	for k := range a {
		if !reflect.DeepEqual(a[k], b[k]) {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func yamlOf(v any) string {
	b, _ := yaml.Marshal(v)
	return string(b)
}

func yamlMap(v any) map[string]any {
	if _, ok := v.(string); ok {
		return nil
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(yamlOf(v)), &m); err != nil {
		return nil
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func (r *run) exec(sql string, args ...any) error {
	_, err := r.tx.Exec(r.ctx, sql, args...)
	return wrap(err, "exec %q", sql)
}

func (r *run) deleteWithGrants(table, resType, id string) error {
	if err := r.dropGrants(resType, id); err != nil {
		return err
	}
	return r.exec("DELETE FROM "+table+" WHERE id = $1 AND managed", id)
}

// mustExist reports a reference to a row that neither the database nor this
// apply (rows are written before their dependents) provides.
func (r *run) mustExist(line int, what, table, id string) {
	var ok bool
	if err := r.tx.QueryRow(r.ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1)", id).Scan(&ok); err != nil || !ok {
		r.errs.add(line, "%s: %s %q does not exist", what, table, id)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
