package engine

// ask_user in a run that can be resumed does not wait in memory. The loop stores the question card and a checkpoint,
// and the run parks: its goroutine ends and its row waits, with the time the question is given. An answer, a cancel
// or that time has the run claimed again, on any instance, and the loop reads the answer where it left off.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/tools"
)

// errParked: the run stored a question and stops until it is answered.
var errParked = errors.New("waiting for the user's answer")

// asksUser reports the built-in question to the user; a tool server may have a tool of the same name.
func asksUser(table map[string]toolEntry, c provider.ToolCall) bool {
	t, ok := table[c.Name]
	return ok && c.Name == "ask_user" && t.provider.ID() == "builtin:core"
}

// question handles an ask_user call. With an answer, or when the question's time has passed, it returns the call's
// result. Otherwise it stores the question (save) and returns errParked. nil, nil: the arguments ask nothing, and the
// tool itself says what is wrong with them.
func (e *Engine) question(x *exec, c provider.ToolCall, open *openRound, save func() error) (*tools.Result, error) {
	card, err := askCard(c.Arguments)
	if err != nil {
		open.Parked = ""
		return nil, nil
	}
	answer, err := e.takeAnswer(x, c.ID)
	if err != nil {
		return nil, err
	}
	if answer != nil {
		open.Parked = "" // the checkpoint that records the result also closes the question in the row
		x.run.dropPending(c.ID)
		res := noAnswer()
		if string(answer) != "null" {
			res = askResult(answer)
		}
		return &res, nil
	}
	pc := pendingFor(card)
	pc.CallID = c.ID
	x.run.addPending(pc)
	open.Parked = c.ID
	if err := save(); err != nil {
		return nil, err
	}
	x.run.card(pc) // after it is stored, so a reload right after the card shows it
	return nil, errParked
}

// takeAnswer reads the answer to the run's open question: nil while it is open, or when callID was never asked, and
// JSON null when nobody answered in time. A question whose time has passed is closed by the same statement, so that
// an answer arriving later is refused instead of being accepted and then ignored.
func (e *Engine) takeAnswer(x *exec, callID string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var asked bool
	var answer []byte
	err := e.DB.QueryRow(ctx, `UPDATE run SET answer = CASE WHEN wait_key = $4 AND answer IS NULL AND wake_at <= now()
			THEN 'null'::jsonb ELSE answer END
		WHERE id = $1 AND lease_owner = $2 AND attempt = $3 RETURNING coalesce(wait_key = $4, false), answer`,
		x.run.ID, e.instance, x.run.attempt, callID).Scan(&asked, &answer)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, errLostLease
	case err != nil:
		return nil, fmt.Errorf("read the answer: %w", err)
	case !asked:
		return nil, nil
	}
	return answer, nil
}

// park lets go of a run that waits for an answer: nothing of it stays in memory. If the answer or a cancel arrived
// while it was stopping, it is queued again at once.
func (e *Engine) park(x *exec) {
	e.saveTrace(x)
	e.detach(x.run, errParked)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var state string
	err := e.DB.QueryRow(ctx, `UPDATE run SET lease_owner = NULL, lease_until = NULL,
			state = CASE WHEN answer IS NULL AND NOT cancel_requested THEN 'waiting' ELSE 'queued' END
		WHERE id = $1 AND lease_owner = $2 AND attempt = $3 RETURNING state`, x.run.ID, e.instance, x.run.attempt).Scan(&state)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("park a run", "run", x.run.ID, "err", err) // the lease expires, and the resumed run parks again
	}
	if state == "queued" {
		e.kick()
	}
}

// Answer delivers the answer to the question a parked run waits on and has the run claimed again. It does not check
// who is answering: the caller does (the HTTP route lets only the chat's owner). ErrNoSuchCall: no such question is
// open on this message.
func (e *Engine) Answer(ctx context.Context, messageID, callID string, value json.RawMessage) error {
	if len(value) == 0 {
		value = json.RawMessage("null")
	}
	// one statement: the row takes the answer and the stored message loses the card
	tag, err := e.DB.Exec(ctx, `WITH answered AS (
			UPDATE run SET answer = $3::jsonb, not_before = NULL, state = CASE WHEN state = 'waiting' THEN 'queued' ELSE state END
			WHERE message_id = $1 AND wait_key = $2 AND answer IS NULL AND NOT cancel_requested RETURNING message_id)
		UPDATE message SET updated_at = now(), pending_calls = coalesce((SELECT jsonb_agg(c) FROM jsonb_array_elements(pending_calls) c
			WHERE c->>'callId' IS DISTINCT FROM $2), '[]'::jsonb)
		WHERE id IN (SELECT message_id FROM answered)`, messageID, callID, []byte(value))
	if err != nil {
		return fmt.Errorf("answer the question: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoSuchCall
	}
	e.kick()
	return nil
}

// hasRun reports whether the user's message has a run.
func (e *Engine) hasRun(ctx context.Context, userID, chatID, messageID string) (bool, error) {
	var yes bool
	if err := e.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM run WHERE message_id = $1 AND chat_id = $2 AND user_id = $3)`,
		messageID, chatID, userID).Scan(&yes); err != nil {
		return false, fmt.Errorf("look up the run: %w", err)
	}
	return yes, nil
}
