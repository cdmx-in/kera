// Package gworkspace syncs Kera users and departments from Google Workspace
// (Admin SDK Directory API, read-only, through a service account with
// domain-wide delegation that impersonates a Workspace admin).
//
// Each run:
//   - creates every active Workspace user who has no Kera account, linked to
//     their Google login (oauth_sub = <oauth_provider>@<directory id>, which is
//     the OIDC sub), with the default sign-up role;
//   - refreshes name and email of matched users;
//   - disables users who are suspended or archived, and enables them again
//     when they are active;
//   - disables Kera users of the Workspace's domains who are no longer listed
//     (deleted) and deletes them after delete_after_days;
//   - puts each user in the Kera group named after their Department.
//
// Settings: connectors.google_workspace.{enabled, interval_minutes,
// credentials (secret), admin_email, oauth_provider, exclude, delete_after_days}.
package gworkspace

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/connectors"
	"github.com/cdmx-in/kera/internal/identity"
	"github.com/cdmx-in/kera/internal/ids"
)

// Name is the connector name and settings prefix.
const Name = "google_workspace"

// Origin marks the users, groups and memberships this sync created.
const Origin = "gworkspace"

const scope = "https://www.googleapis.com/auth/admin.directory.user.readonly"

// directoryURL is a variable so tests can point it at a stub.
var directoryURL = "https://admin.googleapis.com/admin/directory/v1/users"

func init() {
	connectors.Register(connectors.Connector{Name: Name, Title: "Google Workspace", Run: run})
}

// User is the part of a Directory user the sync reads.
type User struct {
	ID           string `json:"id"`
	PrimaryEmail string `json:"primaryEmail"`
	Name         struct {
		FullName string `json:"fullName"`
	} `json:"name"`
	Suspended     bool `json:"suspended"`
	Archived      bool `json:"archived"`
	Organizations []struct {
		Department string `json:"department"`
		Primary    bool   `json:"primary"`
	} `json:"organizations"`
}

// Department is the primary organization's department, else the first one set.
func (u User) Department() string {
	d := ""
	for _, o := range u.Organizations {
		if o.Primary && strings.TrimSpace(o.Department) != "" {
			return strings.TrimSpace(o.Department)
		}
		if d == "" {
			d = strings.TrimSpace(o.Department)
		}
	}
	return d
}

// Options are the settings a sync runs with.
type Options struct {
	Provider    string          // oauth provider id, the oauth_sub prefix
	Exclude     map[string]bool // lower-case emails left alone
	DeleteAfter time.Duration
	DefaultRole string
	Now         time.Time
}

func run(ctx context.Context, a *app.App, dryRun bool) (*connectors.Report, error) {
	creds, err := apply.ReadSecret(ctx, a.Settings, a.Keys, "connectors."+Name+".credentials")
	if err != nil {
		return nil, err
	}
	var admin string
	_ = a.Settings.Get(ctx, "connectors."+Name+".admin_email", &admin)
	if creds == "" || admin == "" {
		return nil, errors.New("Set the service-account key and the admin email first.")
	}
	if strings.HasSuffix(strings.ToLower(admin), ".gserviceaccount.com") {
		return nil, errors.New("Admin to act as must be a Workspace admin user (for example admin@yourdomain), not the service account's own address.")
	}
	client, err := httpClient(ctx, creds, admin)
	if err != nil {
		return nil, err
	}
	users, err := List(ctx, client)
	if err != nil {
		return nil, err
	}
	opt := Options{Provider: "google", DefaultRole: "user", Now: time.Now(), Exclude: map[string]bool{}}
	_ = a.Settings.Get(ctx, "connectors."+Name+".oauth_provider", &opt.Provider)
	days := 30
	_ = a.Settings.Get(ctx, "connectors."+Name+".delete_after_days", &days)
	opt.DeleteAfter = time.Duration(max(days, 1)) * 24 * time.Hour
	var exclude []string
	_ = a.Settings.Get(ctx, "connectors."+Name+".exclude", &exclude)
	for _, e := range exclude {
		opt.Exclude[strings.ToLower(strings.TrimSpace(e))] = true
	}
	_ = a.Settings.Get(ctx, "auth.signup.default_role", &opt.DefaultRole)

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rep := connectors.NewReport()
	if err := Sync(ctx, tx, users, opt, rep); err != nil {
		return rep, err
	}
	if dryRun {
		return rep, nil // the deferred rollback discards everything
	}
	return rep, tx.Commit(ctx)
}

// httpClient authenticates as the service account acting for admin.
func httpClient(ctx context.Context, creds, admin string) (*http.Client, error) {
	var key struct {
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal([]byte(creds), &key); err != nil || key.ClientEmail == "" || key.PrivateKey == "" {
		return nil, errors.New("the credentials are not a service-account JSON key")
	}
	cfg := &jwt.Config{Email: key.ClientEmail, PrivateKey: []byte(key.PrivateKey), PrivateKeyID: key.PrivateKeyID,
		TokenURL: cmp.Or(key.TokenURI, "https://oauth2.googleapis.com/token"), Subject: admin, Scopes: []string{scope}}
	c := cfg.Client(ctx)
	c.Timeout = time.Minute
	return c, nil
}

// List returns every user of the customer, all pages.
func List(ctx context.Context, c *http.Client) ([]User, error) {
	var all []User
	token := ""
	for {
		q := url.Values{"customer": {"my_customer"}, "maxResults": {"500"}, "projection": {"full"},
			"fields": {"nextPageToken,users(id,primaryEmail,name/fullName,suspended,archived,organizations)"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, directoryURL+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if re := (*oauth2.RetrieveError)(nil); errors.As(err, &re) {
			return nil, fmt.Errorf("Google refused the service-account sign-in (%s). Check that the key is current and that "+
				"Domain-wide delegation lists its client id with the scope %s.", cmp.Or(re.ErrorCode, re.Response.Status), scope)
		}
		if err != nil {
			return nil, fmt.Errorf("directory: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("directory: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, directoryError(resp.StatusCode, body)
		}
		var page struct {
			Users         []User `json:"users"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("directory: decode: %w", err)
		}
		all = append(all, page.Users...)
		if token = page.NextPageToken; token == "" {
			return all, nil
		}
	}
}

// directoryError turns a Directory API error into an admin-readable hint.
func directoryError(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := cmp.Or(e.Error.Message, http.StatusText(status))
	hint := ""
	switch status {
	case http.StatusBadRequest:
		hint = " Admin to act as must be an existing Workspace admin user of this domain."
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = " Admin to act as needs the Users > Read admin privilege."
	}
	return fmt.Errorf("Google Workspace answered %d %s.%s", status, msg, hint)
}

type keraUser struct {
	id, email, name, role, sub, origin string
	disabled                           bool
	missingSince                       *time.Time
	listed                             bool
}

func domain(email string) string { return email[strings.LastIndexByte(email, '@')+1:] }

// Sync makes Kera match users inside tx and records every change in rep.
func Sync(ctx context.Context, tx pgx.Tx, users []User, opt Options, rep *connectors.Report) error {
	if len(users) == 0 {
		return errors.New("the directory listed no users; refusing to treat everyone as deleted")
	}
	// Serialise with sign-ups and role changes.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kera.signup')), pg_advisory_xact_lock(hashtext('kera.roles'))`); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT id, lower(email), name, role, coalesce(oauth_sub, ''), origin, disabled_at IS NOT NULL,
		(info->'gworkspace'->>'missing_since')::timestamptz FROM "user"`)
	if err != nil {
		return fmt.Errorf("load users: %w", err)
	}
	var kera []*keraUser
	for rows.Next() {
		k := &keraUser{}
		if err := rows.Scan(&k.id, &k.email, &k.name, &k.role, &k.sub, &k.origin, &k.disabled, &k.missingSince); err != nil {
			rows.Close()
			return err
		}
		kera = append(kera, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	bySub, byEmail := map[string]*keraUser{}, map[string]*keraUser{}
	for _, k := range kera {
		switch {
		case strings.Contains(k.sub, "@"):
			bySub[k.sub] = k
		case k.sub != "": // a bare sub from the Open WebUI import; the next sign-in would add the prefix
			bySub[opt.Provider+"@"+k.sub] = k
		}
		byEmail[k.email] = k
	}

	slices.SortFunc(users, func(a, b User) int { return strings.Compare(a.PrimaryEmail, b.PrimaryEmail) })
	domains := map[string]bool{}
	dept := map[string]string{}   // kera user id -> department
	listed := map[string]string{} // kera user id -> email, for every listed user
	for _, u := range users {
		email := strings.ToLower(u.PrimaryEmail)
		if !strings.Contains(email, "@") || u.ID == "" {
			continue
		}
		domains[domain(email)] = true
		if opt.Exclude[email] {
			continue
		}
		k, err := match(ctx, tx, u, email, opt, bySub, byEmail, rep)
		if err != nil {
			return err
		}
		if k == nil {
			continue
		}
		k.listed = true
		if err := reconcile(ctx, tx, k, u, email, byEmail, rep); err != nil {
			return err
		}
		listed[k.id] = k.email
		if d := u.Department(); d != "" {
			dept[k.id] = d
		}
	}
	if err := prune(ctx, tx, kera, domains, len(users), opt, rep); err != nil {
		return err
	}
	return departments(ctx, tx, dept, listed, rep)
}

// match finds the Kera user for u, linking by email or creating one when needed.
func match(ctx context.Context, tx pgx.Tx, u User, email string, opt Options,
	bySub, byEmail map[string]*keraUser, rep *connectors.Report) (*keraUser, error) {
	sub := opt.Provider + "@" + u.ID
	if k := bySub[sub]; k != nil {
		if k.sub != sub {
			if _, err := tx.Exec(ctx, `UPDATE "user" SET oauth_sub = $2, updated_at = now() WHERE id = $1`, k.id, sub); err != nil {
				return nil, fmt.Errorf("link %s: %w", email, err)
			}
			k.sub = sub
			rep.Add("link", email, "imported login id")
		}
		return k, nil
	}
	if k := byEmail[email]; k != nil {
		if k.sub != "" {
			rep.Warn("%s signs in with another identity (%s); left alone", email, k.sub)
			k.listed = true // not deleted from Workspace either
			return nil, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE "user" SET oauth_sub = $2, updated_at = now() WHERE id = $1`, k.id, sub); err != nil {
			return nil, fmt.Errorf("link %s: %w", email, err)
		}
		k.sub = sub
		bySub[sub] = k
		rep.Add("link", email, "matched by email")
		return k, nil
	}
	if u.Suspended || u.Archived {
		return nil, nil // no account for someone who cannot sign in anyway
	}
	role := "user"
	if opt.DefaultRole == "pending" {
		role = "pending"
	}
	name := cmp.Or(strings.TrimSpace(u.Name.FullName), email[:strings.IndexByte(email, '@')])
	k := &keraUser{id: ids.New(), email: email, name: name, role: role, sub: sub, origin: Origin}
	if _, err := tx.Exec(ctx, `INSERT INTO "user" (id, name, email, role, oauth_sub, origin) VALUES ($1, $2, $3, $4, $5, $6)`,
		k.id, k.name, k.email, k.role, k.sub, Origin); err != nil {
		return nil, fmt.Errorf("create %s: %w", email, err)
	}
	bySub[sub], byEmail[email] = k, k
	rep.Add("create", email, role)
	return k, nil
}

// reconcile brings a matched user's name, email and enabled state in line.
func reconcile(ctx context.Context, tx pgx.Tx, k *keraUser, u User, email string, byEmail map[string]*keraUser, rep *connectors.Report) error {
	if email != k.email {
		if other := byEmail[email]; other != nil && other != k {
			rep.Warn("%s was renamed to %s, which another Kera user has; email left as is", k.email, email)
		} else {
			if _, err := tx.Exec(ctx, `UPDATE "user" SET email = $2, updated_at = now() WHERE id = $1`, k.id, email); err != nil {
				return fmt.Errorf("rename %s: %w", k.email, err)
			}
			rep.Add("update", email, "email was "+k.email)
			delete(byEmail, k.email)
			k.email, byEmail[email] = email, k
		}
	}
	if n := strings.TrimSpace(u.Name.FullName); n != "" && n != k.name {
		if _, err := tx.Exec(ctx, `UPDATE "user" SET name = $2, updated_at = now() WHERE id = $1`, k.id, n); err != nil {
			return fmt.Errorf("rename %s: %w", email, err)
		}
		rep.Add("update", email, "name "+n)
		k.name = n
	}
	inactive := u.Suspended || u.Archived
	switch {
	case inactive && !k.disabled:
		why := "suspended in Workspace"
		if !u.Suspended {
			why = "archived in Workspace"
		}
		return disable(ctx, tx, k, why, nil, rep)
	case !inactive && k.disabled:
		if _, err := tx.Exec(ctx, `UPDATE "user" SET disabled_at = NULL, info = info - 'gworkspace', updated_at = now() WHERE id = $1`, k.id); err != nil {
			return fmt.Errorf("enable %s: %w", email, err)
		}
		k.disabled, k.missingSince = false, nil
		rep.Add("enable", email, "active in Workspace")
	case k.missingSince != nil: // listed again, still suspended
		if _, err := tx.Exec(ctx, `UPDATE "user" SET info = info - 'gworkspace' WHERE id = $1`, k.id); err != nil {
			return fmt.Errorf("clear missing %s: %w", email, err)
		}
		k.missingSince = nil
	}
	return nil
}

// disable locks k out; missing also starts the delete countdown. The last
// active admin is never disabled, so Kera always keeps a way in.
func disable(ctx context.Context, tx pgx.Tx, k *keraUser, why string, missing *time.Time, rep *connectors.Report) error {
	if k.role == "admin" && !k.disabled {
		var admins int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE role = 'admin' AND disabled_at IS NULL`).Scan(&admins); err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if admins <= 1 {
			rep.Warn("%s is the last active admin; not disabled (%s)", k.email, why)
			return nil
		}
	}
	info := `info`
	args := []any{k.id}
	if missing != nil {
		info = `jsonb_set(info, '{gworkspace}', jsonb_build_object('missing_since', $2::timestamptz))`
		args = append(args, *missing)
	}
	if _, err := tx.Exec(ctx, `UPDATE "user" SET disabled_at = coalesce(disabled_at, now()), info = `+info+`, updated_at = now() WHERE id = $1`, args...); err != nil {
		return fmt.Errorf("disable %s: %w", k.email, err)
	}
	if !k.disabled {
		rep.Add("disable", k.email, why)
	} else if missing != nil {
		rep.Add("missing", k.email, why)
	}
	k.disabled, k.missingSince = true, missing
	return nil
}

// prune handles Kera users of the Workspace's domains who are no longer listed.
func prune(ctx context.Context, tx pgx.Tx, kera []*keraUser, domains map[string]bool, listed int, opt Options, rep *connectors.Report) error {
	var gone, expired []*keraUser
	for _, k := range kera {
		if k.listed || !domains[domain(k.email)] || opt.Exclude[k.email] {
			continue
		}
		switch {
		case k.missingSince == nil:
			gone = append(gone, k)
		case opt.Now.Sub(*k.missingSince) >= opt.DeleteAfter:
			expired = append(expired, k)
		}
	}
	// A partial listing (API hiccup, wrong admin scope) must not lock everyone out.
	if limit := max(5, listed/10); len(gone) > limit {
		return fmt.Errorf("%d users would be disabled as missing from Workspace (limit %d); nothing changed. "+
			"Check the admin email and the delegation scope, or exclude these users", len(gone), limit)
	}
	for _, k := range gone {
		now := opt.Now
		if err := disable(ctx, tx, k, fmt.Sprintf("not in Workspace; deleted after %d days", int(opt.DeleteAfter.Hours()/24)), &now, rep); err != nil {
			return err
		}
	}
	for _, k := range expired {
		if k.role == "admin" && !k.disabled {
			continue // the last admin, kept by disable
		}
		// A pipeline's owner cannot be deleted (DeleteUser answers 409). Asking first
		// keeps the sync going: a failed DELETE would abort the transaction and undo
		// every other change of the run. The user was disabled when it went missing.
		var owns bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pipeline WHERE user_id = $1)`, k.id).Scan(&owns); err != nil {
			return fmt.Errorf("check pipelines of %s: %w", k.email, err)
		}
		if owns {
			rep.Warn("%s owns a pipeline; kept disabled until its owner changes", k.email)
			continue
		}
		if _, err := identity.DeleteUser(ctx, tx, k.id); err != nil {
			return fmt.Errorf("delete %s: %w", k.email, err)
		}
		rep.Add("delete", k.email, "missing from Workspace since "+k.missingSince.Format(time.DateOnly))
	}
	return nil
}

// departments puts listed users into the group named after their department.
// Only memberships this sync added (origin gworkspace) are ever removed.
// ponytail: a department that empties keeps its group (and its access grants); an admin deletes it by hand.
func departments(ctx context.Context, tx pgx.Tx, dept map[string]string, listed map[string]string, rep *connectors.Report) error {
	groupID := map[string]string{}
	for _, d := range dept {
		if _, ok := groupID[d]; ok {
			continue
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO "group" (id, name, description, origin) VALUES ($1, $2, 'Google Workspace department', $3)
			ON CONFLICT (name) DO NOTHING RETURNING id`, ids.New(), d, Origin).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `SELECT id FROM "group" WHERE name = $1`, d).Scan(&id)
		} else if err == nil {
			rep.Add("group", d, "created")
		}
		if err != nil {
			return fmt.Errorf("group %s: %w", d, err)
		}
		groupID[d] = id
	}
	want := map[[2]string]bool{}
	for uid, d := range dept {
		want[[2]string{groupID[d], uid}] = true
	}
	rows, err := tx.Query(ctx, `SELECT m.group_id, m.user_id, g.name FROM group_member m JOIN "group" g ON g.id = m.group_id WHERE m.origin = $1`, Origin)
	if err != nil {
		return fmt.Errorf("load memberships: %w", err)
	}
	type member struct{ gid, uid, group string }
	var have []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.gid, &m.uid, &m.group); err != nil {
			rows.Close()
			return err
		}
		have = append(have, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range have {
		key := [2]string{m.gid, m.uid}
		if want[key] {
			delete(want, key)
			continue
		}
		email, ok := listed[m.uid]
		if !ok {
			continue // disabled or missing users keep their groups until they come back or are deleted
		}
		if _, err := tx.Exec(ctx, `DELETE FROM group_member WHERE group_id = $1 AND user_id = $2 AND origin = $3`, m.gid, m.uid, Origin); err != nil {
			return fmt.Errorf("leave %s: %w", m.group, err)
		}
		rep.Add("leave", email, m.group)
	}
	names := map[string]string{}
	for d, id := range groupID {
		names[id] = d
	}
	keys := make([][2]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return cmp.Compare(listed[a[1]], listed[b[1]]) })
	for _, k := range keys {
		tag, err := tx.Exec(ctx, `INSERT INTO group_member (group_id, user_id, origin) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, k[0], k[1], Origin)
		if err != nil {
			return fmt.Errorf("join %s: %w", names[k[0]], err)
		}
		if tag.RowsAffected() > 0 {
			rep.Add("join", listed[k[1]], names[k[0]])
		}
	}
	return nil
}
