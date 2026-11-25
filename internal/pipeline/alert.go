package pipeline

// Alerts for pipelines, replacing the cron probe: when pipelines.alert_url is set (an Alertmanager
// /api/v2/alerts endpoint), every minute the ticker posts one alert per enabled pipeline that is not healthy
// (its last run failed, or it is late). Alertmanager resolves an alert that is not re-sent within its
// resolve_timeout (5 minutes by default), so a repeat each minute keeps it firing; when the pipeline recovers
// one alert with an end time resolves it at once. The name and labels are the ones the old probe used
// (KBPipelineUnhealthy, job=<pipeline>), so existing routes and silences keep working.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

type amAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt,omitempty"`
}

func (s *Service) alertTick(ctx context.Context, now time.Time) {
	var target string
	if err := s.a.Settings.Get(ctx, "pipelines.alert_url", &target); err != nil || target == "" {
		return
	}
	if u, err := url.Parse(target); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		slog.Warn("pipeline: pipelines.alert_url is not an http(s) URL")
		return
	}
	rows, err := s.db.Query(ctx, `SELECT `+pipelineCols+` FROM pipeline WHERE enabled ORDER BY name`)
	if err != nil {
		slog.Error("pipeline: alerts: list", "err", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Pipeline, error) { return scanPipeline(r) })
	if err != nil {
		slog.Error("pipeline: alerts: read", "err", err)
		return
	}
	stamp := now.UTC().Format(time.RFC3339)
	var out []amAlert
	s.alertMu.Lock()
	if s.alerting == nil {
		s.alerting = map[string]bool{}
	}
	for _, p := range list {
		h, good, err := s.healthOf(ctx, p, now)
		if err != nil {
			continue
		}
		a := amAlert{Labels: map[string]string{"alertname": "KBPipelineUnhealthy", "severity": "warning", "job": p.Name}, StartsAt: stamp}
		switch {
		case !good:
			detail := h.Detail
			if detail == "" {
				detail = h.Status
			}
			a.Annotations = map[string]string{"summary": fmt.Sprintf("Pipeline %s is not healthy: %s", p.Name, capText(detail, 400))}
			s.alerting[p.Name] = true
		case s.alerting[p.Name]: // recovered: resolve it now rather than wait for the timeout
			a.Annotations, a.EndsAt = map[string]string{"summary": "Pipeline " + p.Name + " is healthy again"}, stamp
			delete(s.alerting, p.Name)
		default:
			continue
		}
		out = append(out, a)
	}
	s.alertMu.Unlock()
	if len(out) == 0 {
		return
	}
	body, _ := json.Marshal(out)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("pipeline: alerts not sent", "err", err.Error())
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("pipeline: alerts refused", "status", resp.StatusCode)
	}
}
