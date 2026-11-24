package identity

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

// bootstrapAdmin creates the first admin from KERA_ADMIN_EMAIL and
// KERA_ADMIN_PASSWORD, so a fresh install has a way in without shell access. It
// acts only while the user table is empty: it never changes, resets or adds to an
// install that has users, which makes it safe to leave the two variables set
// (and safe to start two replicas at once). The password is never logged.
func bootstrapAdmin(ctx context.Context, a *app.App) error {
	email, password := a.Env.AdminEmail, a.Env.AdminPassword
	if email == "" && password == "" {
		return nil
	}
	if email == "" || password == "" {
		slog.Warn("no admin created: KERA_ADMIN_EMAIL and KERA_ADMIN_PASSWORD must both be set")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var users bool
	if err := a.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "user")`).Scan(&users); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if users {
		slog.Info("KERA_ADMIN_EMAIL is ignored: the install already has users (remove KERA_ADMIN_EMAIL and KERA_ADMIN_PASSWORD from the environment)")
		return nil
	}
	email, err := normalizeEmail(email)
	if err != nil {
		return fmt.Errorf("KERA_ADMIN_EMAIL: %w", err)
	}
	if err := auth.CheckPassword(password, email); err != nil {
		return fmt.Errorf("KERA_ADMIN_PASSWORD: %w", err)
	}
	hash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return fmt.Errorf("bootstrap admin: hash password: %w", err)
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	defer tx.Rollback(ctx)
	// The same lock as the first OAuth sign-up, so only one first user is ever made.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kera.signup'))`); err != nil {
		return fmt.Errorf("bootstrap admin: lock: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "user")`).Scan(&users); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if users { // another process was faster
		return nil
	}
	id := ids.New()
	name, _, _ := strings.Cut(email, "@")
	if _, err := tx.Exec(ctx, `INSERT INTO "user" (id, name, email, role) VALUES ($1, $2, $3, 'admin')`, id, name, email); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_password (user_id, hash) VALUES ($1, $2)`, id, hash); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	slog.Info("first admin created from KERA_ADMIN_EMAIL", "email", email, "user", id)
	return nil
}
