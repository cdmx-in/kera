package pipeline

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestAlertsFireRepeatAndResolveForUnhealthyPipelines(t *testing.T) {
	e := newEnv(t, false)
	var mu sync.Mutex
	var posts [][]amAlert
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var a []amAlert
		if err := json.Unmarshal(b, &a); err != nil {
			t.Errorf("alert body: %v", err)
		}
		mu.Lock()
		posts = append(posts, a)
		mu.Unlock()
	}))
	defer am.Close()
	last := func() []amAlert {
		mu.Lock()
		defer mu.Unlock()
		if len(posts) == 0 {
			return nil
		}
		return posts[len(posts)-1]
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(posts) }

	p, _ := e.mkPipe("docs")
	ctx := t.Context()
	e.s.tick(ctx, e.clk.now()) // no alert_url yet: nothing is sent
	if count() != 0 {
		t.Fatal("alerts sent without pipelines.alert_url")
	}
	if err := e.s.a.Settings.Set(ctx, "pipelines.alert_url", am.URL, false); err != nil {
		t.Fatal(err)
	}
	e.s.tick(ctx, e.clk.now()) // never run: healthy by definition, nothing to say
	if count() != 0 {
		t.Fatalf("a pipeline that never ran alerted: %+v", last())
	}

	run := p.start(startIn{})
	p.finish(run, "boom")
	e.clk.add(time.Minute)
	e.s.tick(ctx, e.clk.now())
	a := last()
	if len(a) != 1 || a[0].Labels["alertname"] != "KBPipelineUnhealthy" || a[0].Labels["job"] != "docs" || a[0].EndsAt != "" || a[0].Annotations["summary"] == "" {
		t.Fatalf("firing alert: %+v", a)
	}
	n := count()
	e.clk.add(time.Minute)
	e.s.tick(ctx, e.clk.now()) // repeated while it stays unhealthy: Alertmanager would resolve it after 5 minutes otherwise
	if count() != n+1 || len(last()) != 1 || last()[0].EndsAt != "" {
		t.Fatalf("repeat: %d posts, %+v", count(), last())
	}

	run = p.start(startIn{})
	p.finish(run, "")
	e.clk.add(time.Minute)
	e.s.tick(ctx, e.clk.now())
	a = last()
	if len(a) != 1 || a[0].EndsAt == "" {
		t.Fatalf("recovery must resolve at once: %+v", a)
	}
	n = count()
	e.clk.add(time.Minute)
	e.s.tick(ctx, e.clk.now())
	if count() != n {
		t.Fatalf("a healthy pipeline keeps alerting: %+v", last())
	}

	// late: a pipeline expected hourly with no run for hours
	e.call(e.admin, "PUT", "/api/v1/admin/pipelines/docs", map[string]any{"every_minutes": 60}, http.StatusOK, nil)
	e.clk.add(5 * time.Hour)
	e.s.tick(ctx, e.clk.now())
	if a = last(); len(a) != 1 || a[0].EndsAt != "" || count() != n+1 {
		t.Fatalf("late pipeline: %d posts, %+v", count(), a)
	}
}
