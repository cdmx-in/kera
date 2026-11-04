package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/apply"
)

// spendPool is a lazily opened, read-only pool on the spend source database
// (2 connections), reopened when the DSN setting changes.
type spendPool struct {
	mu   sync.Mutex
	dsn  string
	pool *pgxpool.Pool
}

func (s *spendPool) get(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pool != nil && s.dsn == dsn {
		return s.pool, nil
	}
	if s.pool != nil {
		go s.pool.Close() // waits for requests still using it
		s.pool = nil
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("spend source dsn: %w", err) // pgx keeps the password out of this error
	}
	cfg.MaxConns = 2
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open spend source: %w", err)
	}
	s.dsn, s.pool = dsn, pool
	return pool, nil
}

// spendRow is one user x model x UTC day; EndUser is "" for calls without a
// user (embeddings, unattributed).
type spendRow struct {
	EndUser          string  `json:"end_user"`
	Model            string  `json:"model"`
	Day              string  `json:"day"`
	Spend            float64 `json:"spend"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Requests         int64   `json:"requests"`
}

type spendUser struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	LastActiveAt int64  `json:"last_active_at"`
}

const litellmSpend = `SELECT COALESCE(end_user, ''), COALESCE(model, ''), to_char("startTime", 'YYYY-MM-DD'),
	COALESCE(sum(spend), 0)::float8, COALESCE(sum(prompt_tokens), 0)::bigint, COALESCE(sum(completion_tokens), 0)::bigint, count(*)
	FROM "LiteLLM_SpendLogs" WHERE "startTime" >= $1 AND "startTime" < $2 GROUP BY 1, 2, 3`

// getSpend serves {start_date, end_date, rows, users}: LiteLLM spend log
// totals per user, model and UTC day, plus every Kera user so the page can
// show people without spend. LiteLLM's startTime is UTC without a zone.
func (h *handler) getSpend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := parseRange(r, true)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var kind string
	_ = h.a.Settings.Get(ctx, "spend.source.kind", &kind)
	dsn, err := apply.ReadSecret(ctx, h.a.Settings, h.a.Keys, "spend.source.dsn")
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if kind != "litellm_postgres" || dsn == "" {
		api.Fail(w, r, api.Errf(http.StatusServiceUnavailable, "spend_not_configured",
			"Spend source not configured (spend.source.kind litellm_postgres and spend.source.dsn)"))
		return
	}
	rows, err := h.spendRows(ctx, dsn, d)
	if err != nil {
		slog.Error("spend source", "err", err)
		api.Fail(w, r, api.Errf(http.StatusBadGateway, "spend_source_failed", "Cannot read the spend source"))
		return
	}
	urows, err := h.a.DB.Query(ctx, `SELECT id, name, email, extract(epoch FROM last_active_at)::bigint FROM "user" ORDER BY created_at`)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	users, err := pgx.CollectRows(urows, func(row pgx.CollectableRow) (spendUser, error) {
		var u spendUser
		return u, row.Scan(&u.ID, &u.Name, &u.Email, &u.LastActiveAt)
	})
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list users: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"start_date": d.start.Format(time.DateOnly), "end_date": d.end.Format(time.DateOnly),
		"rows": rows, "users": nonNil(users)})
}

func (h *handler) spendRows(ctx context.Context, dsn string, d dateRange) ([]spendRow, error) {
	pool, err := h.spend.get(ctx, dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// timestamp without time zone: pass wall-clock UTC values.
	rows, err := pool.Query(ctx, litellmSpend, d.start.Format(time.DateTime), d.until().Format(time.DateTime))
	if err != nil {
		return nil, fmt.Errorf("query spend logs: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (spendRow, error) {
		var x spendRow
		return x, row.Scan(&x.EndUser, &x.Model, &x.Day, &x.Spend, &x.PromptTokens, &x.CompletionTokens, &x.Requests)
	})
	if err != nil {
		return nil, fmt.Errorf("read spend logs: %w", err)
	}
	return nonNil(out), nil
}
