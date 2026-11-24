package admin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Retrieval tab's extractor card: the address in effect (the environment's default until a value is
// stored) and whether it answers, with the cause when it does not.
func TestExtractorStatus(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	status := func() extractorOut {
		t.Helper()
		var out extractorOut
		e.ok("GET", "/api/v1/configs/extractor/status", nil, &out)
		return out
	}
	if out := status(); out.URL != "" || out.OK || out.Error != "" {
		t.Fatalf("nothing configured: %+v", out)
	}

	ex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","types":[".docx",".pdf"],"ocr":false,"ocr_langs":"eng"}`))
	}))
	defer ex.Close()
	e.a.Settings.SetDefault("rag.extractor.url", ex.URL+"/")
	out := status()
	if !out.OK || out.URL != ex.URL || strings.Join(out.Types, " ") != ".docx .pdf" || out.OCR == nil || *out.OCR || out.OCRLangs != "eng" {
		t.Fatalf("answering extractor: %+v", out)
	}
	// the settings list shows the same address as the default and as the value
	var list []settingOut
	e.ok("GET", "/api/v1/configs/settings", nil, &list)
	if s := findSetting(t, list, "rag.extractor.url"); string(s.Default) != `"`+ex.URL+`/"` || string(s.Value) != string(s.Default) || s.Set {
		t.Fatalf("setting = %+v", s)
	}

	// a stored value wins; a service that is not an extractor is named as such
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if err := e.a.Settings.Set(ctx, "rag.extractor.url", other.URL, false); err != nil {
		t.Fatal(err)
	}
	if out := status(); out.OK || out.URL != other.URL || !strings.Contains(out.Error, "HTTP 404") {
		t.Fatalf("not an extractor: %+v", out)
	}

	// nothing listening
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()
	if err := e.a.Settings.Set(ctx, "rag.extractor.url", dead, false); err != nil {
		t.Fatal(err)
	}
	if out := status(); out.OK || !strings.Contains(out.Error, "not reachable: connection refused") {
		t.Fatalf("down: %+v", out)
	}
}
