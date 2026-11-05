package guard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Moderator classifies text and returns the flagged category names (none =
// clean). nil means no moderation model is reachable for this connection.
type Moderator func(ctx context.Context, text string) ([]string, error)

// OpenAIModerator calls POST <base>/v1/moderations (OpenAI and gateways that
// route a moderation model) and returns the categories marked true.
func OpenAIModerator(hc *http.Client, baseURL, apiKey, model string) Moderator {
	if hc == nil {
		hc = http.DefaultClient
	}
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return func(ctx context.Context, text string) ([]string, error) {
		if len(text) > 32000 {
			text = text[:32000]
		}
		body, _ := json.Marshal(map[string]any{"model": model, "input": text})
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/moderations", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		res, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode/100 != 2 {
			return nil, fmt.Errorf("moderation: HTTP %d", res.StatusCode)
		}
		var out struct {
			Results []struct {
				Flagged    bool            `json:"flagged"`
				Categories map[string]bool `json:"categories"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || len(out.Results) == 0 {
			return nil, fmt.Errorf("moderation: unexpected answer")
		}
		var cats []string
		for k, v := range out.Results[0].Categories {
			if v {
				cats = append(cats, k)
			}
		}
		if out.Results[0].Flagged && len(cats) == 0 {
			cats = []string{"flagged"}
		}
		sort.Strings(cats)
		return cats, nil
	}
}
