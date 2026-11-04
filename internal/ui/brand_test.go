package ui

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/config"
)

const svgDoc = `<?xml version="1.0"?><!-- logo --><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`

func TestBrandFileName(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	for _, tc := range []struct {
		slot, file string
		data       []byte
		want, code string
	}{
		{"logo", "My Logo.png", pngPixel, "logo-" + sha8(pngPixel) + ".png", ""},
		{"", "My Logo!.PNG", pngPixel, "my-logo-" + sha8(pngPixel) + ".png", ""},
		{"", `..\..\etc/passwd.png`, pngPixel, "passwd-" + sha8(pngPixel) + ".png", ""},
		{"../../x", "a.png", pngPixel, "x-" + sha8(pngPixel) + ".png", ""},
		{"", ".png", pngPixel, "image-" + sha8(pngPixel) + ".png", ""},
		{"", "photo.jpeg", jpeg, "photo-" + sha8(jpeg) + ".jpg", ""},
		{"mark", "m.svg", []byte(svgDoc), "mark-" + sha8([]byte(svgDoc)) + ".svg", ""},
		{"", "fake.png", []byte("<html><script>x</script>"), "", "bad_type"}, // content does not match
		{"", "fake.svg", []byte("<html><svg/></html>"), "", "bad_type"},      // root is not <svg>
		{"", "png-as.svg", pngPixel, "", "bad_type"},
		{"", "page.html", []byte("<svg/>"), "", "bad_type"},
		{"", "anim.gif", []byte("GIF89a......"), "", "bad_type"},
		{"", "empty.png", nil, "", "empty"},
		{"", "big.png", append(append([]byte{}, pngPixel...), make([]byte, maxBrandUpload)...), "", "too_large"},
	} {
		got, err := brandFileName(tc.slot, tc.file, tc.data)
		var e *api.Error
		switch {
		case tc.code == "" && (err != nil || got != tc.want):
			t.Errorf("%q/%q = %q, %v; want %q", tc.slot, tc.file, got, err, tc.want)
		case tc.code != "" && (err == nil || !asErr(err, &e) || e.Code != tc.code):
			t.Errorf("%q/%q: err %v, want %s", tc.slot, tc.file, err, tc.code)
		}
	}
}

func asErr(err error, e **api.Error) bool {
	x, ok := err.(*api.Error)
	*e = x
	return ok
}

// adminSetup is setup with the brand admin routes (no auth wrapper) and an
// uploads dir.
func adminSetup(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "brand")
	up := filepath.Join(dir, "data", "brand")
	must(t, os.Mkdir(cfgDir, 0o755))
	must(t, os.WriteFile(filepath.Join(cfgDir, "mark.png"), pngPixel, 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(testYAML), 0o644))
	srv, err := api.New(config.Env{}, nil) // no database: these routes do not read settings
	must(t, err)
	u := &UI{assets: &assets{upload: up, dir: cfgDir}, tmpl: []byte(shell), configFile: filepath.Join(dir, "config.yaml")}
	u.routes(srv)
	u.adminRoutes(srv, func(f http.HandlerFunc) http.Handler { return f })
	return srv.Handler(), up, cfgDir
}

func upload(h http.Handler, slot, name string, data []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if slot != "" {
		_ = mw.WriteField("slot", slot)
	}
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(data)
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/api/v1/admin/brand/assets", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestBrandUploads(t *testing.T) {
	h, up, cfgDir := adminSetup(t)

	// upload: stored in the uploads dir under a content-addressed name
	w := upload(h, "logo", "x.png", pngPixel)
	var out assetOut
	must(t, json.Unmarshal(w.Body.Bytes(), &out))
	name := "logo-" + sha8(pngPixel) + ".png"
	if w.Code != 200 || out.URL != "/brand/"+name || out.Dir != "uploads" {
		t.Fatalf("upload = %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(up, name)); err != nil {
		t.Fatal(err)
	}
	if w := upload(h, "", "x.svg", []byte("<script>alert(1)</script>")); w.Code != 400 {
		t.Errorf("script as svg: %d", w.Code)
	}
	if w := upload(h, "", "big.png", append(append([]byte{}, pngPixel...), make([]byte, 3<<20)...)); w.Code != 413 {
		t.Errorf("3 MB upload: %d %s", w.Code, w.Body)
	}

	// lookup: the uploads dir shadows the config dir
	other := []byte(svgDoc)
	must(t, os.WriteFile(filepath.Join(up, "mark.png"), append(append([]byte{}, pngPixel...), 0), 0o644))
	must(t, os.WriteFile(filepath.Join(cfgDir, "only-config.svg"), other, 0o644))
	if w := get(h, "/brand/mark.png"); w.Code != 200 || w.Body.Len() != len(pngPixel)+1 {
		t.Errorf("mark.png from uploads: %d, %d bytes", w.Code, w.Body.Len())
	}
	if w := get(h, "/brand/only-config.svg"); w.Code != 200 || w.Header().Get("Content-Security-Policy") != "sandbox" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("config fallback: %d %v", w.Code, w.Header())
	}
	if w := get(h, "/brand/"+name+"?v="+sha8(pngPixel)); w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("uploaded file cache = %q", w.Header().Get("Cache-Control"))
	}

	// list: both dirs, with where each file lives
	var list []assetOut
	must(t, json.Unmarshal(get(h, "/api/v1/admin/brand/assets").Body.Bytes(), &list))
	where := map[string]string{}
	for _, a := range list {
		where[a.Dir+"/"+a.File] = a.URL
	}
	for _, k := range []string{"uploads/" + name, "uploads/mark.png", "config/mark.png", "config/only-config.svg"} {
		if where[k] == "" {
			t.Errorf("list lacks %s: %v", k, list)
		}
	}

	// delete: uploads only, no traversal
	del := func(file string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/admin/brand/assets/"+file, nil))
		return w.Code
	}
	if c := del("only-config.svg"); c != 404 {
		t.Errorf("delete config file: %d", c)
	}
	if c := del("..%2fbrand%2fmark.png"); c != 404 {
		t.Errorf("delete traversal: %d", c)
	}
	must(t, os.Symlink(filepath.Join(cfgDir, "mark.png"), filepath.Join(up, "link.png")))
	if c := del("link.png"); c != 404 {
		t.Errorf("delete symlink: %d", c)
	}
	if c := del(name); c != 204 {
		t.Errorf("delete upload: %d", c)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "mark.png")); err != nil {
		t.Error("config file gone")
	}
	if c := del("mark.png"); c != 204 || get(h, "/brand/mark.png").Body.Len() != len(pngPixel) {
		t.Errorf("after deleting the upload, mark.png should come from the config dir again")
	}

	// the config file's brand section, for "reset"
	w = get(h, "/api/v1/admin/brand/config")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Example"`) || !strings.Contains(w.Body.String(), `"logoDark":"/brand/missing.png"`) {
		t.Errorf("config brand = %d %s", w.Code, w.Body)
	}
}
