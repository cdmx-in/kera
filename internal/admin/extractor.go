package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/probe"
)

// extractorOut answers GET /api/v1/configs/extractor/status (always HTTP 200; ok says whether the
// document extractor answered). url is the address in effect, the stored rag.extractor.url or else
// KERA_EXTRACTOR_URL, and empty when there is none. Older extractors do not report ocr.
type extractorOut struct {
	URL      string   `json:"url"`
	OK       bool     `json:"ok"`
	MS       int64    `json:"ms"`
	Error    string   `json:"error,omitempty"`
	Types    []string `json:"types,omitempty"`
	OCR      *bool    `json:"ocr,omitempty"`
	OCRLangs string   `json:"ocr_langs,omitempty"`
}

// ponytail: checks the address in effect, not one typed into the form and not yet saved; a POST that takes a url
// (like the other Test buttons) would let an admin try an address before saving it.
func (h *handler) extractorStatus(w http.ResponseWriter, r *http.Request) {
	var base string
	_ = h.a.Settings.Get(r.Context(), "rag.extractor.url", &base)
	out := extractorOut{URL: strings.TrimRight(strings.TrimSpace(base), "/")}
	if out.URL == "" {
		api.JSON(w, http.StatusOK, out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), probe.Timeout)
	defer cancel()
	start := time.Now()
	status, body, err := get(ctx, probe.Client(), http.MethodGet, out.URL+"/health", nil, nil)
	out.MS = time.Since(start).Milliseconds()
	var health struct {
		Status   string   `json:"status"`
		Types    []string `json:"types"`
		OCR      *bool    `json:"ocr"`
		OCRLangs string   `json:"ocr_langs"`
	}
	switch {
	case err != nil:
		out.Error = "not reachable: " + files.ExtractorCause(err)
	case status != http.StatusOK:
		out.Error = fmt.Sprintf("answered HTTP %d, which a document extractor does not: check the URL", status)
	case json.Unmarshal(body, &health) != nil || health.Status != "ok":
		out.Error = "answered, but not like a document extractor: check the URL"
	default:
		out.OK, out.Types, out.OCR, out.OCRLangs = true, health.Types, health.OCR, health.OCRLangs
	}
	api.JSON(w, http.StatusOK, out)
}
