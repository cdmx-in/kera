package engine

// Every run of a stored chat has a row in the run table while it is live. The instance that works on it holds a lease
// it renews; a run whose lease expired is claimed by whoever notices, and every write carries the owner and the
// attempt number, so an owner that was too slow cannot touch what the next one writes. See docs/durable-runs.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/models"
)

// How often the owner renews its leases, how long a lease lasts without a renewal, and how many expired leases of
// one run are taken over before it is given up (a run that keeps taking the process down must not come back forever).
// ponytail: constants (tests shorten them); make them settings if a deployment needs another trade-off between
// resuming soon and riding out a database that stalls.
var (
	beatEvery     = 10 * time.Second
	leaseFor      = 30 * time.Second
	maxRecoveries = 3
)

// orphanAfter is how long a reply may be marked as being written with no run row before the janitor fails it.
var orphanAfter = time.Minute

// crashedMsg is the error of a reply whose run could not be continued.
const crashedMsg = "The server restarted while this answer was being written."

// errLostLease: this instance no longer owns the run. It stops and writes nothing.
var errLostLease = errors.New("another instance owns this run now")

// storedRequest is what is needed to build a run again: the request without its conversation. It carries no
// credentials: generation parameters are the decoded whitelist (paramKeys), and no headers or tokens are kept.
type storedRequest struct {
	Model       string          `json:"model"`
	Interactive bool            `json:"interactive,omitempty"` // stream_format "events": ask_user is offered
	Local       bool            `json:"local,omitempty"`       // the model was local when the run started
	ParentID    string          `json:"parent_id,omitempty"`   // the message the answer replies to
	OwnMessages bool            `json:"own_messages,omitempty"`
	ClientTools bool            `json:"client_tools,omitempty"`
	Files       []FileAttach    `json:"files,omitempty"`
	Features    map[string]bool `json:"features"` // null and {} differ (the model's defaults, or none)
	ToolIDs     []string        `json:"tool_ids"` // null and [] differ (the model's servers, or none)
	Params      map[string]any  `json:"params,omitempty"`
	Unattended  bool            `json:"unattended,omitempty"`
	ReadOnly    bool            `json:"read_only,omitempty"`
	Preface     string          `json:"preface,omitempty"`
}

func storedOf(x *exec) storedRequest {
	r := x.req
	return storedRequest{Model: r.Model, Interactive: x.interactive, Local: x.m.Local, ParentID: x.parentID,
		OwnMessages: r.UserMessage == nil && len(r.Messages) > 0, ClientTools: len(r.Tools) > 0, Files: r.Files,
		Features: r.Features, ToolIDs: r.ToolIDs, Params: r.Params, Unattended: r.Unattended, ReadOnly: r.ReadOnly,
		Preface: r.Preface}
}

// busy reports whether a run, here or on another instance, owns the message.
func (e *Engine) busy(ctx context.Context, messageID string) (bool, error) {
	var yes bool
	if err := e.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM run WHERE message_id = $1)`, messageID).Scan(&yes); err != nil {
		return false, fmt.Errorf("look up the run: %w", err)
	}
	return yes, nil
}

// insertRun records a new run, owned by this instance. The message row must exist.
func (e *Engine) insertRun(ctx context.Context, x *exec) error {
	req, err := json.Marshal(storedOf(x))
	if err != nil {
		return fmt.Errorf("encode the run's request: %w", err)
	}
	var limit float64
	if x.req.Unattended {
		limit = e.unattendedLimit(ctx).Seconds()
	}
	_, err = e.DB.Exec(ctx, `INSERT INTO run (id, message_id, chat_id, user_id, request, state, lease_owner, lease_until, deadline)
		VALUES ($1, $2, $3, $4, $5, 'running', $6, now() + make_interval(secs => $7),
			CASE WHEN $8::float8 > 0 THEN now() + make_interval(secs => $8::float8) END)`,
		x.run.ID, x.run.MessageID, x.chat.ID, x.u.ID, req, e.instance, leaseFor.Seconds(), limit)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" { // unique_violation: another start won
		return errBusy
	}
	if err != nil {
		return fmt.Errorf("record the run: %w", err)
	}
	x.run.setLeased()
	return nil
}

// dropRun removes the row of a run that never started.
func (e *Engine) dropRun(x *exec) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := e.DB.Exec(ctx, `DELETE FROM run WHERE id = $1 AND lease_owner = $2 AND attempt = $3`,
		x.run.ID, e.instance, x.run.attempt); err != nil {
		slog.Warn("delete the row of a run that did not start", "run", x.run.ID, "err", err)
	}
}

// fence is what this attempt's message writes carry.
func (e *Engine) fence(x *exec) *chats.Fence {
	return &chats.Fence{RunID: x.run.ID, Owner: e.instance, Attempt: x.run.attempt}
}

// hold renews the run's lease, which also proves it: while the lease is fresh nobody else can claim the run. It is
// the guard before something that cannot be fenced in its own statement. errLostLease: another instance owns the run
// (or its chat is gone). errCancelled: someone asked to stop it, from any instance. Runs without a row pass.
func (e *Engine) hold(ctx context.Context, x *exec) error {
	if x.chat == nil {
		return nil
	}
	var cancelled bool
	err := e.DB.QueryRow(ctx, `UPDATE run SET lease_until = now() + make_interval(secs => $4)
		WHERE id = $1 AND lease_owner = $2 AND attempt = $3 RETURNING cancel_requested`,
		x.run.ID, e.instance, x.run.attempt, leaseFor.Seconds()).Scan(&cancelled)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errLostLease
	case err != nil:
		return fmt.Errorf("renew the run's lease: %w", err)
	case cancelled:
		return errCancelled
	}
	return nil
}

// endRun writes the outcome of a run with a row in one transaction: the row goes, which locks it and proves the
// ownership, and the message, the chat's locality and the usage are written. errLostLease: nothing was written.
func (e *Engine) endRun(ctx context.Context, x *exec, p chats.Progress, ok bool, u *usageRow) error {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("end run: %w", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM run WHERE id = $1 AND lease_owner = $2 AND attempt = $3`,
		x.run.ID, e.instance, x.run.attempt)
	if err != nil {
		return fmt.Errorf("end run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errLostLease
	}
	if err := chats.SaveProgressIn(ctx, tx, x.run.MessageID, p); err != nil {
		return err
	}
	if ok {
		if err := chats.SetLocalityIn(ctx, tx, x.chat.ID, x.m.Local); err != nil {
			return err
		}
	}
	if u != nil {
		if err := insertUsage(ctx, tx, *u); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// usageRow is one row of the usage table.
type usageRow struct {
	userID, modelID string
	chatID, msgID   *string
	usage           Usage
	duration        time.Duration
}

func insertUsage(ctx context.Context, db chats.Execer, u usageRow) error {
	if _, err := db.Exec(ctx, `INSERT INTO usage (user_id, model_id, chat_id, message_id, prompt_tokens, completion_tokens, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, u.userID, u.modelID, u.chatID, u.msgID, u.usage.PromptTokens, u.usage.CompletionTokens,
		u.duration.Milliseconds()); err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// abandon stops serving a run this instance no longer owns: nothing is written, and attached streams close without
// done, so that a browser reconnects.
func (e *Engine) abandon(x *exec) {
	slog.Warn("run lease lost, leaving the run to its new owner", "run", x.run.ID)
	e.detach(x.run, errLostLease)
}

// detach takes a run that goes on elsewhere (or later) out of this instance.
func (e *Engine) detach(r *Run, why error) {
	r.detach(why)
	e.runs.remove(r)
}

// ---- heartbeat and claims -------------------------------------------------------------

// durableLoop renews the leases and takes over runs that need an owner, until the engine stops.
func (e *Engine) durableLoop() {
	t := time.NewTicker(beatEvery)
	defer t.Stop()
	for {
		e.tick(context.Background())
		select {
		case <-t.C:
		case <-e.kicked:
		}
	}
}

// kick makes the loop look for work now.
func (e *Engine) kick() {
	select {
	case e.kicked <- struct{}{}:
	default:
	}
}

// tick is one round of the loop: renew, then claim. No claim after a failed renewal: an instance that cannot keep
// its own leases must not take more.
func (e *Engine) tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, beatEvery)
	defer cancel()
	if err := e.beat(ctx); err != nil {
		slog.Warn("run heartbeat", "err", err)
		return
	}
	for {
		c, ok, err := e.claim(ctx)
		if err != nil {
			slog.Warn("claim a run", "err", err)
		}
		if !ok {
			return
		}
		e.takeOver(c)
	}
}

// beat renews the leases of this instance's live runs and tells them what changed: a run whose row is gone or has a
// new owner is stopped, and so is one that someone asked to cancel. Only the runs alive here are renewed, so a row
// whose goroutine is gone expires and is taken over. After leaseFor without a successful renewal every run here is
// stopped: another instance may already have claimed them.
func (e *Engine) beat(ctx context.Context) error {
	sent := time.Now()
	local := e.runs.leased()
	if len(local) == 0 {
		e.lastBeat = sent
		return nil
	}
	got := map[string]struct {
		attempt   int
		cancelled bool
	}{}
	ids := make([]string, 0, len(local))
	for id := range local {
		ids = append(ids, id)
	}
	err := func() error {
		rows, err := e.DB.Query(ctx, `UPDATE run SET lease_until = now() + make_interval(secs => $2)
			WHERE lease_owner = $1 AND state = 'running' AND id = ANY($3) RETURNING id, attempt, cancel_requested`,
			e.instance, leaseFor.Seconds(), ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var v struct {
				attempt   int
				cancelled bool
			}
			if err := rows.Scan(&id, &v.attempt, &v.cancelled); err != nil {
				return err
			}
			got[id] = v
		}
		return rows.Err()
	}()
	if err != nil {
		if time.Since(e.lastBeat) >= leaseFor {
			for _, r := range local {
				r.stop(errLostLease)
			}
		}
		return err
	}
	e.lastBeat = sent
	for id, r := range local {
		v, ok := got[id]
		switch {
		case !ok || v.attempt != r.attempt:
			// also true for a run that ended since the list was made; stopping an ended run does nothing
			r.stop(errLostLease)
		case v.cancelled:
			r.stop(errCancelled)
		}
	}
	return nil
}

// claimed is a run this instance just took over.
type claimed struct {
	id, messageID, chatID, userID string
	origin                        string
	request, checkpoint           []byte
	attempt, recoveries           int
	cancelled                     bool
}

// claim takes one run that needs an owner: queued, with an expired lease, or waiting past its wake time.
func (e *Engine) claim(ctx context.Context) (claimed, bool, error) {
	var c claimed
	err := e.DB.QueryRow(ctx, `UPDATE run SET state = 'running', lease_owner = $1, lease_until = now() + make_interval(secs => $2),
			attempt = attempt + 1, recoveries = recoveries + (state = 'running')::int
		WHERE id = (SELECT id FROM run WHERE state = 'queued'
				OR (state = 'running' AND lease_until < now())
				OR (state = 'waiting' AND wake_at <= now())
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, message_id, chat_id, user_id, origin, request, checkpoint, attempt, recoveries, cancel_requested`,
		e.instance, leaseFor.Seconds()).Scan(&c.id, &c.messageID, &c.chatID, &c.userID, &c.origin, &c.request, &c.checkpoint,
		&c.attempt, &c.recoveries, &c.cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// takeOver deals with a claimed run. A run cannot be continued yet, so it ends: stopped when someone asked for that,
// otherwise with the error of an interrupted answer.
func (e *Engine) takeOver(c claimed) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := e.Chats.Message(ctx, c.chatID, c.messageID)
	if err != nil {
		slog.Warn("claimed run: load the message", "run", c.id, "err", err)
		return // the lease expires and the run is claimed again
	}
	if m.Status != "streaming" { // it ended; only the row was left
		if _, err := e.DB.Exec(ctx, `DELETE FROM run WHERE id = $1 AND lease_owner = $2 AND attempt = $3`,
			c.id, e.instance, c.attempt); err != nil {
			slog.Warn("delete the row of an ended run", "run", c.id, "err", err)
		}
		return
	}
	x := e.claimedExec(c, m.ModelID)
	restoreDisplay(x.run, m)
	if c.cancelled {
		e.end(x, "stopped", false)
		return
	}
	x.run.fail("interrupted", crashedMsg)
	e.end(x, "done", false)
}

// claimedExec is the working state of a claimed run before anything else of it is rebuilt.
func (e *Engine) claimedExec(c claimed, modelID string) *exec {
	run := newRun(c.chatID, c.messageID, c.userID, modelID, true)
	run.ID, run.attempt = c.id, c.attempt
	run.base = int64(c.attempt-1) << 32
	if old := e.runs.replace(run); old != nil {
		old.stop(errLostLease) // a stale attempt of ours that has not noticed yet
		old.detach(errLostLease)
	}
	run.setLeased()
	return &exec{u: &auth.User{ID: c.userID}, m: &models.Resolved{ID: modelID}, run: run,
		chat: &chats.Chat{ID: c.chatID, UserID: c.userID}, req: &Request{}, started: time.Now(), disabledNames: map[string]bool{}}
}

// restoreDisplay puts the stored message back into a run, so its streams and saves continue from it.
func restoreDisplay(r *Run, m chats.Message) {
	st := msgState{Status: "streaming"}
	decode := func(raw json.RawMessage, dst any) {
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, dst); err != nil {
				slog.Warn("run: stored message state", "run", r.ID, "err", err)
			}
		}
	}
	decode(m.Parts, &st.Parts)
	decode(m.Files, &st.Files)
	decode(m.Sources, &st.Sources)
	decode(m.FollowUps, &st.FollowUps)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = st
}

// ---- cancel ---------------------------------------------------------------------------

// requestCancel records a cancel on the message's run, so the instance that owns it (any of them) stops it. It
// reports whether the user has such a run.
func (e *Engine) requestCancel(ctx context.Context, userID, chatID, messageID string) (bool, error) {
	tag, err := e.DB.Exec(ctx, `UPDATE run SET cancel_requested = true,
			state = CASE WHEN state = 'waiting' THEN 'queued' ELSE state END
		WHERE message_id = $1 AND chat_id = $2 AND user_id = $3`, messageID, chatID, userID)
	if err != nil {
		return false, fmt.Errorf("cancel run: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---- janitor --------------------------------------------------------------------------

// failOrphans fails replies that are marked as being written but have no run: nothing will ever finish them (a run
// of an older version, or a copied message). It replaces a start-up sweep, which with two instances would have
// failed the other one's live answers.
func (e *Engine) failOrphans(ctx context.Context) (int64, error) {
	tag, err := e.DB.Exec(ctx, `UPDATE message SET status = 'error', error = $1, pending_calls = '[]', updated_at = now()
		WHERE status = 'streaming' AND updated_at < now() - make_interval(secs => $2)
			AND NOT EXISTS (SELECT 1 FROM run WHERE run.message_id = message.id)`, crashedMsg, orphanAfter.Seconds())
	if err != nil {
		return 0, fmt.Errorf("fail orphaned replies: %w", err)
	}
	return tag.RowsAffected(), nil
}
