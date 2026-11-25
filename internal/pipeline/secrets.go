package pipeline

// The central secret store: a named, encrypted value that a pipeline environment variable can reference
// (pipeline_env.secret_id) instead of keeping its own copy. Values are write-only: no route returns one, nor
// anything derived from it. Admin only, like the rest of the pipeline admin API.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

const maxSecrets = 200

// secretNameRe is the env-name alphabet in lower case, plus '-' and '.': "read-token", "svc.api_key".
var secretNameRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9._-]{0,99}$`)

type usedBy struct {
	Pipeline string `json:"pipeline"`
	Variable string `json:"variable"`
}

type secretOut struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	UsedBy      []usedBy   `json:"used_by"`
	UpdatedBy   string     `json:"updated_by"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

// usedByOf lists the variables that reference a secret (every secret when name is empty), keyed by secret name.
func (s *Service) usedByOf(ctx context.Context, name string) (map[string][]usedBy, error) {
	rows, err := s.db.Query(ctx, `SELECT sc.name, p.name, e.name FROM pipeline_env e
		JOIN secret sc ON sc.id = e.secret_id JOIN pipeline p ON p.id = e.pipeline_id
		WHERE $1 = '' OR sc.name = $1 ORDER BY p.name, e.name`, name)
	if err != nil {
		return nil, fmt.Errorf("secret usage: %w", err)
	}
	defer rows.Close()
	out := map[string][]usedBy{}
	for rows.Next() {
		var sn string
		var u usedBy
		if err := rows.Scan(&sn, &u.Pipeline, &u.Variable); err != nil {
			return nil, err
		}
		out[sn] = append(out[sn], u)
	}
	return out, rows.Err()
}

func (s *Service) aSecrets(w http.ResponseWriter, r *http.Request) {
	s.secretsReply(w, r, http.StatusOK)
}

func (s *Service) secretsReply(w http.ResponseWriter, r *http.Request, status int) {
	use, err := s.usedByOf(r.Context(), "")
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT sc.name, sc.description, coalesce(u.email, ''), sc.updated_at, sc.last_used_at
		FROM secret sc LEFT JOIN "user" u ON u.id = coalesce(sc.updated_by, sc.created_by) ORDER BY sc.name`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list secrets: %w", err))
		return
	}
	defer rows.Close()
	out := []secretOut{}
	for rows.Next() {
		var o secretOut
		if err := rows.Scan(&o.Name, &o.Description, &o.UpdatedBy, &o.UpdatedAt, &o.LastUsedAt); err != nil {
			api.Fail(w, r, err)
			return
		}
		o.UsedBy = use[o.Name]
		if o.UsedBy == nil {
			o.UsedBy = []usedBy{}
		}
		o.UpdatedAt = o.UpdatedAt.UTC()
		out = append(out, o)
	}
	api.JSON(w, status, map[string]any{"secrets": out})
}

// secretValueErr checks a value to store: text, not empty, at most 16 KiB, no NUL.
func secretValueErr(v string) error {
	if v == "" || len(v) > maxEnvValue || strings.ContainsRune(v, 0) {
		return api.FieldErr("value", "bad_value", "A value of at most 16 KiB of text, not empty")
	}
	return nil
}

func (s *Service) secretSeal(v string) ([]byte, error) {
	if err := secretValueErr(v); err != nil {
		return nil, err
	}
	return s.a.Keys.Encrypt([]byte(v))
}

func userID(r *http.Request) *string {
	if u := auth.FromContext(r.Context()); u != nil {
		return &u.ID
	}
	return nil
}

func (s *Service) aSecretCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Value       string `json:"value"`
		// From copies the value of a pipeline's secret variable into the store, inside the database: the value
		// never leaves the server (the way to store a value that only Kera still holds).
		From *struct{ Pipeline, Variable string } `json:"from"`
	}
	if err := decode(w, r, &in, maxEnvValue+4096); err != nil {
		api.Fail(w, r, err)
		return
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if !secretNameRe.MatchString(in.Name) {
		api.Fail(w, r, api.FieldErr("name", "bad_name", "1 to 100 lowercase letters, digits, dashes, dots and underscores"))
		return
	}
	if len(in.Description) > 500 {
		api.Fail(w, r, api.FieldErr("description", "bad_description", "At most 500 characters"))
		return
	}
	var enc []byte
	var err error
	if in.From != nil {
		if in.Value != "" {
			api.Fail(w, r, api.FieldErr("value", "value_and_from", "Give a value or a variable to copy, not both"))
			return
		}
		enc, err = s.variableCipher(r.Context(), in.From.Pipeline, in.From.Variable)
	} else {
		enc, err = s.secretSeal(in.Value)
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var n int
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM secret`).Scan(&n)
	if n >= maxSecrets {
		api.Fail(w, r, api.FieldErr("name", "too_many", fmt.Sprintf("At most %d secrets", maxSecrets)))
		return
	}
	now, by := s.now(), userID(r)
	if _, err := s.db.Exec(r.Context(), `INSERT INTO secret (id, name, description, value_enc, created_by, created_at, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $5, $6)`, ids.New(), in.Name, in.Description, enc, by, now); err != nil {
		if strings.Contains(err.Error(), "23505") {
			api.Fail(w, r, api.FieldErr("name", "name_taken", "A secret with this name exists"))
			return
		}
		api.Fail(w, r, fmt.Errorf("create secret: %w", err))
		return
	}
	slog.Info("secret created", "name", in.Name, "by", deref(by, "")) // never the value
	s.secretsReply(w, r, http.StatusCreated)
}

// variableCipher is the stored ciphertext of a secret variable (its own, or that of the secret it points at).
func (s *Service) variableCipher(ctx context.Context, pipeline, variable string) ([]byte, error) {
	var enc []byte
	err := s.db.QueryRow(ctx, `SELECT coalesce(e.value_enc, sc.value_enc) FROM pipeline_env e JOIN pipeline p ON p.id = e.pipeline_id
		LEFT JOIN secret sc ON sc.id = e.secret_id WHERE p.name = $1 AND e.name = $2 AND e.secret`, pipeline, variable).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, api.FieldErr("from", "unknown_variable", "That pipeline has no secret variable with this name")
	}
	return enc, err
}

// aSecretUpdate changes the value (write-only) and/or the description. Pipelines that reference the secret
// read the new value when their next run starts.
func (s *Service) aSecretUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Description *string `json:"description"`
		Value       *string `json:"value"`
	}
	if err := decode(w, r, &in, maxEnvValue+4096); err != nil {
		api.Fail(w, r, err)
		return
	}
	if in.Value == nil && in.Description == nil {
		api.Fail(w, r, api.FieldErr("value", "empty", "Send a new value, a description, or both"))
		return
	}
	var enc []byte
	if in.Value != nil {
		var err error
		if enc, err = s.secretSeal(*in.Value); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len(d) > 500 {
			api.Fail(w, r, api.FieldErr("description", "bad_description", "At most 500 characters"))
			return
		}
		in.Description = &d
	}
	name, by := r.PathValue("name"), userID(r)
	tag, err := s.db.Exec(r.Context(), `UPDATE secret SET value_enc = coalesce($2, value_enc), description = coalesce($3, description),
		updated_by = $4, updated_at = $5 WHERE name = $1`, name, enc, in.Description, by, s.now())
	if err != nil {
		api.Fail(w, r, fmt.Errorf("update secret: %w", err))
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Secret not found"))
		return
	}
	slog.Info("secret updated", "name", name, "value_changed", in.Value != nil, "by", deref(by, ""))
	s.secretsReply(w, r, http.StatusOK)
}

func (s *Service) aSecretDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	use, err := s.usedByOf(r.Context(), name)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if u := use[name]; len(u) > 0 {
		var parts []string
		for _, x := range u {
			parts = append(parts, x.Pipeline+"/"+x.Variable)
		}
		api.JSON(w, http.StatusConflict, map[string]any{"code": "in_use", "used_by": u,
			"detail": "Still used by " + strings.Join(parts, ", ") + ". Remove or change those variables first."})
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM secret WHERE name = $1`, name)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("delete secret: %w", err)) // includes a variable that pointed at it a moment ago (RESTRICT)
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Secret not found"))
		return
	}
	slog.Info("secret deleted", "name", name, "by", deref(userID(r), ""))
	w.WriteHeader(http.StatusNoContent)
}

// secretID finds a stored secret by name; ok is false when there is none.
func (s *Service) secretID(ctx context.Context, name string) (id string, ok bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT id FROM secret WHERE name = $1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}
