// Package toolserver turns tool_server rows into tools.Providers: OpenAPI
// servers (spec -> function specs, HTTP calls) and MCP servers (Streamable
// HTTP client). Auth is none, a static bearer/header key, or the user's own
// OAuth grant (user_oauth, via internal/oauthclient).
package toolserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/oauthclient"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/tools"
)

func init() { app.Register("toolserver", Mount) }

const (
	rowsTTL  = 10 * time.Second // tool_server rows are re-read this often
	specsTTL = 5 * time.Minute  // function lists per server and user
	// listTimeout bounds one function listing, so a hung server delays a chat run by at most this.
	// ponytail: failures are not cached; each run retries a dead server. Cache errors briefly if that hurts.
	listTimeout = 15 * time.Second
)

// Mount registers the database-backed provider source and GET /api/v1/tools/.
func Mount(a *app.App) error {
	m := NewManager(a.DB, a.Keys, a.Settings, files.For(a), oauthclient.For(a))
	tools.RegisterSource(m.Providers)
	h := &handler{m: m, all: tools.All}
	a.Server.Handle("GET /api/v1/tools/{$}", a.Auth.Required(http.HandlerFunc(h.list)))
	a.Server.Handle("GET /api/v1/tools", a.Auth.Required(http.HandlerFunc(h.list)))
	return nil
}

// Row is one tool_server row with its secret decrypted.
type Row struct {
	ID          string
	Kind        string // openapi | mcp
	Name        string
	Description string
	URL         string
	SpecPath    string
	Auth        Auth
	NameFilter  []string
	Disabled    []string
	MaxChars    int
	AutoEnable  bool
	Display     json.RawMessage
}

// Auth is tool_server.auth: none | bearer (Key) | header (Header: Key) |
// user_oauth (Provider; optionally Header: Key as well, for a server that
// needs its own service key next to the user's token in Authorization).
type Auth struct {
	Type     string `json:"type"`
	KeyEnc   string `json:"key_enc,omitempty"`
	Header   string `json:"header,omitempty"`
	Provider string `json:"provider,omitempty"`
	key      string
}

// Manager owns one Server per enabled row and reloads rows every few seconds.
type Manager struct {
	db       *pgxpool.Pool
	keys     *secret.Keys
	settings *settings.Store
	files    *files.Service
	oauth    *oauthclient.Service
	http     *http.Client

	mu       sync.Mutex
	servers  []*Server
	sums     map[string][32]byte // row checksum per server id: a changed row gets a fresh Server
	loadedAt time.Time
}

// NewManager builds a manager. fs and oc may be nil in tests that need neither.
func NewManager(db *pgxpool.Pool, keys *secret.Keys, st *settings.Store, fs *files.Service, oc *oauthclient.Service) *Manager {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	// no client timeout: MCP calls may wait minutes for the user; contexts bound them
	m := &Manager{db: db, keys: keys, settings: st, files: fs, oauth: oc, sums: map[string][32]byte{},
		http: &http.Client{Transport: tr}}
	if oc != nil {
		oc.OnChange(m.forget)
	}
	return m
}

// Providers is the tools.RegisterSource function.
func (m *Manager) Providers(ctx context.Context) ([]tools.Provider, error) {
	ss, err := m.Servers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Provider, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out, nil
}

// Servers returns the enabled servers in sort order.
func (m *Manager) Servers(ctx context.Context) ([]*Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.loadedAt) < rowsTTL && m.servers != nil {
		return m.servers, nil
	}
	rows, err := m.load(ctx)
	if err != nil {
		if m.servers != nil { // a database hiccup must not take every chat's tools down
			slog.Warn("tool servers: reload failed, keeping the previous set", "err", err)
			m.loadedAt = time.Now()
			return m.servers, nil
		}
		return nil, err
	}
	old := map[string]*Server{}
	for _, s := range m.servers {
		old[s.row.ID] = s
	}
	sums := map[string][32]byte{}
	servers := make([]*Server, 0, len(rows))
	for _, r := range rows {
		b, _ := json.Marshal(r)
		sum := sha256.Sum256(append(b, r.Auth.key...))
		sums[r.ID] = sum
		if s := old[r.ID]; s != nil && m.sums[r.ID] == sum {
			servers = append(servers, s)
			continue
		}
		servers = append(servers, newServer(m, r))
	}
	m.servers, m.sums, m.loadedAt = servers, sums, time.Now()
	return servers, nil
}

func (m *Manager) load(ctx context.Context) ([]Row, error) {
	q, err := m.db.Query(ctx, `SELECT id, kind, name, description, url, spec_path, auth, name_filter, disabled_functions,
		max_result_chars, auto_enable_when_connected, display FROM tool_server WHERE enabled ORDER BY sort, id`)
	if err != nil {
		return nil, fmt.Errorf("load tool servers: %w", err)
	}
	defer q.Close()
	var out []Row
	for q.Next() {
		var r Row
		var authRaw []byte
		if err := q.Scan(&r.ID, &r.Kind, &r.Name, &r.Description, &r.URL, &r.SpecPath, &authRaw, &r.NameFilter,
			&r.Disabled, &r.MaxChars, &r.AutoEnable, &r.Display); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(authRaw, &r.Auth)
		if r.Auth.Type == "" {
			r.Auth.Type = "none"
		}
		if r.Auth.KeyEnc != "" {
			sealed, err := base64.StdEncoding.DecodeString(r.Auth.KeyEnc)
			if err == nil {
				r.Auth.key, err = m.keys.DecryptString(sealed)
			}
			if err != nil { // skip only this server: tools.All failing would break every chat
				slog.Error("tool server skipped: key undecryptable (re-run kera apply after a secret change)", "server", r.ID, "err", err)
				continue
			}
		}
		out = append(out, r)
	}
	return out, q.Err()
}

// forget drops cached function lists and MCP sessions of a user for every
// server bound to provider (called on connect and disconnect).
func (m *Manager) forget(userID, provider string) {
	m.mu.Lock()
	ss := m.servers
	m.mu.Unlock()
	for _, s := range ss {
		if s.row.Auth.Type == "user_oauth" && s.row.Auth.Provider == provider {
			s.forget(userID)
		}
	}
}

// maxChars is the server's cap, else tools.max_result_chars.
func (m *Manager) maxChars(ctx context.Context, r Row) int {
	if r.MaxChars > 0 {
		return r.MaxChars
	}
	n := 0
	if m.settings != nil {
		_ = m.settings.Get(ctx, "tools.max_result_chars", &n)
	}
	return n
}

// ErrNotConnected is returned by Execute when a user_oauth server's user has
// no grant; the result then tells the model to ask the user to connect.
var ErrNotConnected = oauthclient.ErrNotConnected

// Server is one tool server, a tools.Provider.
type Server struct {
	m   *Manager
	row Row
	mcp *mcpPool // nil for OpenAPI

	mu    sync.Mutex
	specs map[string]cachedSpecs // user id -> functions
}

type cachedSpecs struct {
	specs []tools.Spec
	ops   map[string]*operation // OpenAPI only
	until time.Time
}

func newServer(m *Manager, r Row) *Server {
	s := &Server{m: m, row: r, specs: map[string]cachedSpecs{}}
	if r.Kind == "mcp" {
		s.mcp = newMCPPool(m.http, r.URL)
	}
	return s
}

// ID implements tools.Provider.
func (s *Server) ID() string { return s.row.ID }

// Row returns the server's configuration.
func (s *Server) Row() Row { return s.row }

func (s *Server) forget(userID string) {
	s.mu.Lock()
	delete(s.specs, userID)
	s.mu.Unlock()
	if s.mcp != nil {
		s.mcp.drop(userID)
	}
}

// CanRead reports the user's read access to the server.
func (s *Server) CanRead(ctx context.Context, u *auth.User) (bool, error) {
	return access.Can(ctx, s.m.db, u, access.ToolServer, s.row.ID, access.Read)
}

// Authenticated reports whether u can call the server now (always true
// unless the server needs the user's own OAuth grant).
func (s *Server) Authenticated(ctx context.Context, u *auth.User) (bool, error) {
	if s.row.Auth.Type != "user_oauth" {
		return true, nil
	}
	_, err := s.token(ctx, u)
	if errors.Is(err, ErrNotConnected) {
		return false, nil
	}
	return err == nil, err
}

// Tools implements tools.Provider: nil without read access or, for a
// user_oauth server, before the user connected.
func (s *Server) Tools(ctx context.Context, u *auth.User) ([]tools.Spec, error) {
	c, err := s.cached(ctx, u)
	if errors.Is(err, errSkip) {
		return nil, nil
	}
	return c.specs, err
}

var errSkip = errors.New("no access")

func (s *Server) cached(ctx context.Context, u *auth.User) (cachedSpecs, error) {
	ok, err := s.CanRead(ctx, u)
	if err != nil {
		return cachedSpecs{}, err
	}
	if !ok {
		return cachedSpecs{}, errSkip
	}
	tok, err := s.token(ctx, u)
	if errors.Is(err, ErrNotConnected) {
		return cachedSpecs{}, errSkip
	}
	if err != nil {
		return cachedSpecs{}, err
	}
	s.mu.Lock()
	c, hit := s.specs[u.ID]
	s.mu.Unlock()
	if hit && time.Now().Before(c.until) {
		return c, nil
	}
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	if s.mcp != nil {
		c, err = s.listMCP(lctx, u, tok)
	} else {
		c, err = s.listOpenAPI(lctx, tok)
	}
	if err != nil {
		return cachedSpecs{}, fmt.Errorf("tool server %s: %w", s.row.ID, err)
	}
	c.specs = s.filter(c.specs)
	c.until = time.Now().Add(specsTTL)
	s.mu.Lock()
	s.specs[u.ID] = c
	s.mu.Unlock()
	return c, nil
}

// filter applies name_filter (glob allow-list, empty = all) and disabled_functions.
func (s *Server) filter(in []tools.Spec) []tools.Spec {
	out := in[:0:0]
	for _, sp := range in {
		if s.allowed(sp.Name) {
			out = append(out, sp)
		}
	}
	return out
}

func (s *Server) allowed(name string) bool {
	if slices.Contains(s.row.Disabled, name) {
		return false
	}
	if len(s.row.NameFilter) == 0 {
		return true
	}
	for _, p := range s.row.NameFilter {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// token returns the credential sent to the server: the user's OAuth access
// token for user_oauth, else "" (static keys are added by authorize).
func (s *Server) token(ctx context.Context, u *auth.User) (string, error) {
	if s.row.Auth.Type != "user_oauth" {
		return "", nil
	}
	if s.m.oauth == nil || u == nil {
		return "", ErrNotConnected
	}
	return s.m.oauth.AccessToken(ctx, u.ID, s.row.Auth.Provider)
}

// authorize adds the server's credential to a request.
func (s *Server) authorize(h http.Header, tok string) {
	switch s.row.Auth.Type {
	case "bearer":
		h.Set("Authorization", "Bearer "+s.row.Auth.key)
	case "header":
		h.Set(s.row.Auth.Header, s.row.Auth.key)
	case "user_oauth":
		h.Set("Authorization", "Bearer "+tok)
		if s.row.Auth.Header != "" && s.row.Auth.key != "" {
			h.Set(s.row.Auth.Header, s.row.Auth.key)
		}
	}
}

// contextHeaders tells the server who calls and how to call back.
func contextHeaders(h http.Header, c tools.Call) {
	set := func(k, v string) {
		if v != "" {
			h.Set(k, v)
		}
	}
	if c.User != nil {
		set("X-Kera-User-Id", c.User.ID)
		set("X-Kera-User-Email", c.User.Email)
	}
	set("X-Kera-Chat-Id", c.ChatID)
	set("X-Kera-Message-Id", c.MessageID)
	set("X-Kera-Files", strings.Join(c.FileIDs, ","))
	set("X-Kera-Callback", c.CallbackURL)
	set("X-Kera-Callback-Token", c.CallbackToken)
}

// Execute implements tools.Provider. Transport failures come back as error
// results the model can read; err is reserved for programming errors.
func (s *Server) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	cs, err := s.cached(ctx, c.User)
	switch {
	case errors.Is(err, errSkip) && s.row.Auth.Type == "user_oauth":
		return errResult("connect %s first: the user has not connected this integration (or the connection expired)", s.label()), nil
	case errors.Is(err, errSkip):
		return errResult("tool server %s is not available to this user", s.row.ID), nil
	case errors.Is(err, errRejected) && s.row.Auth.Type == "user_oauth":
		return s.rejected(), nil
	case err != nil:
		return errResult("%v", err), nil
	}
	if !slices.ContainsFunc(cs.specs, func(sp tools.Spec) bool { return sp.Name == c.Name }) {
		return errResult("unknown tool %q on %s", c.Name, s.row.ID), nil
	}
	tok, err := s.token(ctx, c.User)
	if err != nil {
		return errResult("connect %s first: %v", s.label(), err), nil
	}
	var res tools.Result
	if s.mcp != nil {
		res, err = s.callMCP(ctx, c, tok)
	} else {
		res, err = s.callOpenAPI(ctx, c, cs.ops[c.Name], tok)
	}
	if errors.Is(err, errRejected) && s.row.Auth.Type == "user_oauth" {
		return s.rejected(), nil
	}
	if err != nil {
		return errResult("%s on %s failed: %v", c.Name, s.row.ID, err), nil
	}
	res.Content = Truncate(res.Content, s.m.maxChars(ctx, s.row))
	return res, nil
}

func (s *Server) rejected() tools.Result {
	return errResult("%s rejected the user's connection (revoked or expired): ask the user to disconnect and connect %s again", s.label(), s.label())
}

func (s *Server) label() string {
	if s.row.Name != "" {
		return s.row.Name
	}
	return s.row.ID
}

func errResult(format string, args ...any) tools.Result {
	return tools.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

// Truncate caps s at max characters (0 = no cap) with a note for the model.
func Truncate(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	n := utf8.RuneCountInString(s)
	cut := 0
	for i := range s {
		if max == 0 {
			cut = i
			break
		}
		max--
	}
	return s[:cut] + fmt.Sprintf("\n\n[Truncated: the result had %d characters and only the first %d are shown. "+
		"Ask for less, e.g. a narrower query, a filter or a smaller range.]", n, utf8.RuneCountInString(s[:cut]))
}
