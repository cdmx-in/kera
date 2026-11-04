package importowui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/settings"
)

// maxInlineAvatar is the largest data: URL avatar kept inline.
const maxInlineAvatar = 64 << 10

// loadUserMap resolves every source user to a target user: the same id, else
// an existing user with the same email (case-insensitive) or OAuth subject.
// Unresolved users are absent from the map; rows they own are skipped.
func (im *importer) loadUserMap(ctx context.Context) error {
	rows, err := im.tx.Query(ctx, `SELECT id, lower(email), coalesce(oauth_sub, '') FROM "user"`)
	if err != nil {
		return fmt.Errorf("list target users: %w", err)
	}
	byID, byEmail, bySub := map[string]bool{}, map[string]string{}, map[string]string{}
	var id, email, sub string
	if _, err := pgx.ForEachRow(rows, []any{&id, &email, &sub}, func() error {
		byID[id], byEmail[email] = true, id
		if sub != "" {
			bySub[sub] = id
		}
		return nil
	}); err != nil {
		return fmt.Errorf("list target users: %w", err)
	}
	return im.sourceRows(ctx, `SELECT id, coalesce(email, ''), oauth FROM "user"`, nil, func(r pgx.Rows) error {
		var sid, semail string
		var oauth []byte
		if err := r.Scan(&sid, &semail, &oauth); err != nil {
			return err
		}
		switch {
		case byID[sid]:
			im.users[sid] = sid
		case byEmail[strings.ToLower(semail)] != "":
			im.users[sid] = byEmail[strings.ToLower(semail)]
		case oauthSub(oauth) != "" && bySub[oauthSub(oauth)] != "":
			im.users[sid] = bySub[oauthSub(oauth)]
		}
		return nil
	})
}

// oauthSub picks the provider subject from {"google": {"sub": ...}, ...},
// preferring google, else the first provider by name.
func oauthSub(raw []byte) string {
	var m map[string]map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "google") != (names[j] == "google") {
			return names[i] == "google"
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		if s, ok := m[n]["sub"].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func (im *importer) importUsers(ctx context.Context) error {
	st := im.rep.Stat("users")
	err := im.sourceRows(ctx, `SELECT id, coalesce(name, ''), coalesce(email, ''), coalesce(role, ''), coalesce(profile_image_url, ''),
		settings, info, oauth, last_active_at, created_at, updated_at FROM "user" ORDER BY id`, nil, func(r pgx.Rows) error {
		var id, name, email, role, image string
		var settingsJ, info, oauth []byte
		var lastActive, created, updated *int64
		if err := r.Scan(&id, &name, &email, &role, &image, &settingsJ, &info, &oauth, &lastActive, &created, &updated); err != nil {
			return err
		}
		st.Source++
		if email == "" {
			st.skip("no email", id)
			return nil
		}
		if t, ok := im.users[id]; ok && t != id {
			st.Existing++
			st.note("merged into an existing user with the same email or login")
			st.decide("merge " + id + " -> " + t)
			return nil
		}
		if role != "admin" && role != "pending" {
			role = "user"
		}
		if strings.HasPrefix(image, "data:") && len(image) > maxInlineAvatar {
			image = "" // ponytail: dropped, not stored as a file; the next Google login refreshes it
			st.note("avatar data URL over 64 KB dropped")
		}
		var sub *string
		if s := oauthSub(oauth); s != "" {
			sub = &s
		}
		if _, err := im.upsert(ctx, st, id, `INSERT INTO "user" (id, name, email, role, profile_image_url, oauth_sub, settings, info,
				origin, last_active_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'openwebui', $9, $10, $11)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, email = EXCLUDED.email, role = EXCLUDED.role,
				profile_image_url = EXCLUDED.profile_image_url, oauth_sub = coalesce(EXCLUDED.oauth_sub, "user".oauth_sub),
				settings = EXCLUDED.settings, info = EXCLUDED.info,
				last_active_at = greatest("user".last_active_at, EXCLUDED.last_active_at), updated_at = EXCLUDED.updated_at
			WHERE "user".origin = 'openwebui' AND "user".updated_at <= EXCLUDED.updated_at
			RETURNING (xmax = 0)`,
			id, name, email, role, image, sub, jsonObject(settingsJ), jsonObject(info), ts(lastActive), ts(created), ts(updated)); err != nil {
			return err
		}
		im.users[id] = id
		return nil
	})
	if err != nil {
		return err
	}
	return im.importAPIKeys(ctx)
}

// importAPIKeys stores each plaintext source key as its hash, so the same
// sk- strings keep authenticating.
func (im *importer) importAPIKeys(ctx context.Context) error {
	st := im.rep.Stat("api_keys")
	return im.sourceRows(ctx, `SELECT id, user_id, coalesce(key, ''), data, expires_at, last_used_at, created_at FROM api_key ORDER BY id`, nil,
		func(r pgx.Rows) error {
			var id, userID, key string
			var data []byte
			var expires, lastUsed, created *int64
			if err := r.Scan(&id, &userID, &key, &data, &expires, &lastUsed, &created); err != nil {
				return err
			}
			st.Source++
			uid, ok := im.users[userID]
			switch {
			case !ok:
				st.skip("owner missing", id)
				return nil
			case !auth.IsAPIKey(key):
				st.skip("not an sk- key", id)
				return nil
			case expires != nil && *expires > 0 && epoch(*expires).Before(time.Now()):
				st.skip("expired", id)
				return nil
			}
			name, _ := jsonObject(data)["name"].(string)
			if name == "" {
				name = "Imported key"
			}
			var used any
			if lastUsed != nil && *lastUsed > 0 {
				used = epoch(*lastUsed)
			}
			return im.put(ctx, st, id, `INSERT INTO api_key (id, user_id, key_hash, prefix, name, last_used_at, created_at, origin)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 'openwebui')
				ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, key_hash = EXCLUDED.key_hash, prefix = EXCLUDED.prefix,
					last_used_at = greatest(api_key.last_used_at, EXCLUDED.last_used_at)
				WHERE api_key.origin = 'openwebui'
				RETURNING (xmax = 0)`, id, uid, auth.HashAPIKey(key), auth.KeyPrefix(key), name, used, ts(created))
		})
}

// permAliases renames source permission keys whose meaning matches a Kera key.
var permAliases = map[string]string{"features.memories": "features.memory"}

// knownPermissions lists the dotted keys of Kera's default permission tree.
func knownPermissions() map[string]bool {
	var tree map[string]map[string]any
	_ = json.Unmarshal(settings.Defaults["user.permissions"], &tree)
	out := map[string]bool{}
	for sec, keys := range tree {
		for k := range keys {
			out[sec+"."+k] = true
		}
	}
	return out
}

// mapPermissions keeps the boolean permissions Kera knows and notes the rest.
func mapPermissions(src map[string]any, st *Stat) map[string]any {
	known := knownPermissions()
	out := map[string]any{}
	for sec, v := range src {
		keys, ok := v.(map[string]any)
		if !ok {
			st.note("permission not mapped: " + sec)
			continue
		}
		for k, val := range keys {
			path := sec + "." + k
			if a, ok := permAliases[path]; ok {
				path = a
			}
			b, isBool := val.(bool)
			if !isBool || !known[path] {
				st.note("permission not mapped: " + path)
				continue
			}
			s, key, _ := strings.Cut(path, ".")
			m, _ := out[s].(map[string]any)
			if m == nil {
				m = map[string]any{}
				out[s] = m
			}
			m[key] = b
		}
	}
	return out
}

func (im *importer) importGroups(ctx context.Context) error {
	st := im.rep.Stat("groups")
	rows, err := im.tx.Query(ctx, `SELECT id, name FROM "group"`)
	if err != nil {
		return fmt.Errorf("list target groups: %w", err)
	}
	nameOwner := map[string]string{}
	var gid, gname string
	if _, err := pgx.ForEachRow(rows, []any{&gid, &gname}, func() error { nameOwner[gname] = gid; return nil }); err != nil {
		return fmt.Errorf("list target groups: %w", err)
	}
	err = im.sourceRows(ctx, `SELECT id, coalesce(name, ''), coalesce(description, ''), permissions, created_at, updated_at FROM "group" ORDER BY id`, nil,
		func(r pgx.Rows) error {
			var id, name, desc string
			var perms []byte
			var created, updated *int64
			if err := r.Scan(&id, &name, &desc, &perms, &created, &updated); err != nil {
				return err
			}
			st.Source++
			if owner, ok := nameOwner[name]; ok && owner != id {
				st.skip("name taken by another group", id)
				return nil
			}
			return im.put(ctx, st, id, `INSERT INTO "group" (id, name, description, permissions, origin, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'openwebui', $5, $6)
				ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description,
					permissions = EXCLUDED.permissions, updated_at = EXCLUDED.updated_at
				WHERE "group".origin = 'openwebui' AND NOT "group".managed AND "group".updated_at <= EXCLUDED.updated_at
				RETURNING (xmax = 0)`, id, name, desc, mapPermissions(jsonObject(perms), st), ts(created), ts(updated))
		})
	if err != nil {
		return err
	}
	return im.importGroupMembers(ctx)
}

func (im *importer) importGroupMembers(ctx context.Context) error {
	st := im.rep.Stat("group_members")
	groups, err := im.targetIDs(ctx, `"group"`)
	if err != nil {
		return err
	}
	return im.sourceRows(ctx, `SELECT group_id, user_id FROM group_member ORDER BY group_id, user_id`, nil, func(r pgx.Rows) error {
		var g, u string
		if err := r.Scan(&g, &u); err != nil {
			return err
		}
		st.Source++
		uid, ok := im.users[u]
		switch {
		case !groups[g]:
			st.skip("group missing", g+"/"+u)
			return nil
		case !ok:
			st.skip("user missing", g+"/"+u)
			return nil
		}
		return im.put(ctx, st, g+"/"+u, `INSERT INTO group_member (group_id, user_id, origin) VALUES ($1, $2, 'openwebui')
			ON CONFLICT DO NOTHING RETURNING true`, g, uid)
	})
}

// grantTypes maps source resource types to Kera's; others are skipped.
var grantTypes = map[string]string{"model": "model", "knowledge": "knowledge", "prompt": "prompt", "folder": "folder", "chat": "chat"}

func (im *importer) importGrants(ctx context.Context) error {
	st := im.rep.Stat("grants")
	groups, err := im.targetIDs(ctx, `"group"`)
	if err != nil {
		return err
	}
	return im.sourceRows(ctx, `SELECT id, resource_type, resource_id, principal_type, principal_id, permission, created_at
		FROM access_grant ORDER BY id`, nil, func(r pgx.Rows) error {
		var id, rtype, rid, ptype, pid, perm string
		var created *int64
		if err := r.Scan(&id, &rtype, &rid, &ptype, &pid, &perm, &created); err != nil {
			return err
		}
		st.Source++
		kt, ok := grantTypes[rtype]
		if !ok {
			st.skip("resource type "+rtype+" not in Kera", id)
			return nil
		}
		if perm != "read" && perm != "write" {
			st.skip("permission "+perm+" unknown", id)
			return nil
		}
		switch ptype {
		case "user":
			if pid != "*" {
				uid, ok := im.users[pid]
				if !ok {
					st.skip("principal missing", id)
					return nil
				}
				pid = uid
			}
		case "group":
			if !groups[pid] {
				st.skip("principal missing", id)
				return nil
			}
		default:
			st.skip("principal type "+ptype+" unknown", id)
			return nil
		}
		return im.put(ctx, st, id, `INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission, origin, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'openwebui', $7) ON CONFLICT DO NOTHING RETURNING true`,
			id, kt, rid, ptype, pid, perm, ts(created))
	})
}
