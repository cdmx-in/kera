package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Start re-queues rows left pending/processing by a previous run and starts
// files.workers (default 2) workers. Changing files.workers needs a restart.
func (s *Service) Start(ctx context.Context) error {
	n := 2
	_ = s.settings.Get(ctx, "files.workers", &n)
	if n <= 0 {
		n = 2
	}
	if _, err := s.db.Exec(ctx, `UPDATE file SET status = 'pending', updated_at = now() WHERE status = 'processing'`); err != nil {
		return fmt.Errorf("reset processing files: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM file WHERE status = 'pending' ORDER BY created_at`)
	if err != nil {
		return fmt.Errorf("list pending files: %w", err)
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		s.wg.Add(1)
		go s.work()
	}
	for _, id := range pending {
		s.enqueue(id)
	}
	if len(pending) > 0 {
		slog.Info("files: re-queued unprocessed files", "count", len(pending))
	}
	return nil
}

// Close stops the workers after their current file.
func (s *Service) Close() {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
}

func (s *Service) enqueue(id string) {
	select {
	case s.queue <- id:
	default:
		// Queue full: hand off without blocking the uploader.
		go func() {
			select {
			case s.queue <- id:
			case <-s.stop:
			}
		}()
	}
}

func (s *Service) work() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case id := <-s.queue:
			s.process(id)
		}
	}
}

// process claims one pending file, extracts it and records the outcome.
func (s *Service) process(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	f, err := scanFile(s.db.QueryRow(ctx, `UPDATE file SET status = 'processing', updated_at = now()
		WHERE id = $1 AND status = 'pending' RETURNING `+fileCols, id))
	if errors.Is(err, ErrNotFound) {
		return // already taken, done or deleted
	}
	if err != nil {
		slog.Error("files: claim", "file", id, "err", err)
		return
	}
	text, meta, xerr := s.extract(ctx, f)
	if xerr != nil {
		slog.Warn("files: extraction failed", "file", id, "err", xerr)
		_, err = s.db.Exec(ctx, `UPDATE file SET status = 'failed', error = $2, updated_at = now() WHERE id = $1`, id, xerr.Error())
	} else {
		mj, _ := json.Marshal(meta)
		_, err = s.db.Exec(ctx, `UPDATE file SET status = 'completed', error = '', text = $2, meta = meta || $3, updated_at = now()
			WHERE id = $1`, id, text, mj)
	}
	if err != nil {
		slog.Error("files: save result", "file", id, "err", err)
		return
	}
	if f, err = s.Get(ctx, id); err != nil {
		return // deleted meanwhile
	}
	s.mu.RLock()
	hooks := append([]func(context.Context, File){}, s.hooks...)
	s.mu.RUnlock()
	for _, h := range hooks {
		runHook(ctx, h, f)
	}
}

func runHook(ctx context.Context, h func(context.Context, File), f File) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("files: OnProcessed hook panicked", "file", f.ID, "value", v)
		}
	}()
	h(ctx, f)
}
