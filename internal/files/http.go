package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
)

func init() { app.Register("files", Mount) }

var (
	regMu sync.Mutex
	reg   = map[*app.App]*Service{}
)

// For returns the app's file service, creating it on first use. Features that
// need files (chat engine, knowledge, code interpreter) call this from their
// Mount; workers start when the files feature mounts.
func For(a *app.App) *Service {
	regMu.Lock()
	defer regMu.Unlock()
	if s := reg[a]; s != nil {
		return s
	}
	s := New(a.DB, a.Settings, a.Env.DataDir)
	reg[a] = s
	return s
}

// Mount starts the workers and registers the /api/v1/files routes.
func Mount(a *app.App) error {
	s := For(a)
	if err := s.Start(context.Background()); err != nil {
		return err
	}
	h := &handler{svc: s}
	routes := map[string]http.HandlerFunc{
		"POST /api/v1/files/{$}":                   h.upload,
		"POST /api/v1/files":                       h.upload,
		"GET /api/v1/files/{$}":                    h.list,
		"GET /api/v1/files":                        h.list,
		"GET /api/v1/files/{id}":                   h.get,
		"GET /api/v1/files/{id}/process/status":    h.status,
		"GET /api/v1/files/{id}/content":           h.content,
		"GET /api/v1/files/{id}/content/{name...}": h.content,
		"GET /api/v1/files/{id}/data/content":      h.text,
		"GET /api/v1/files/{id}/preview":           h.preview,
		"DELETE /api/v1/files/{id}":                h.delete,
	}
	for p, fn := range routes {
		a.Server.Handle(p, a.Auth.Required(fn)) // CSRF: see auth.Credential
	}
	return nil
}

type handler struct{ svc *Service }

// JSON returns the Open WebUI-compatible file object.
func JSON(f File) map[string]any {
	data := map[string]any{"status": f.Status}
	if f.Error != "" {
		data["error"] = f.Error
	}
	return map[string]any{"id": f.ID, "user_id": f.UserID, "hash": f.SHA256, "filename": f.Filename,
		"meta": f.Meta, "data": data, "created_at": f.CreatedAt.Unix(), "updated_at": f.UpdatedAt.Unix()}
}

var errNotFound = api.Errf(http.StatusNotFound, "not_found", "File not found")

func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := auth.FromContext(ctx)
	if !u.Can("chat.file_upload") {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Permission required: chat.file_upload"))
		return
	}
	max := h.svc.MaxBytes(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, max+1<<20) // + room for multipart framing
	mr, err := r.MultipartReader()
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", "expected multipart/form-data"))
		return
	}
	process := r.URL.Query().Get("process") != "false"
	var f *File
	var md any
	lateMeta := false
	fail := func(err error) { // drop a blob already stored so no orphan is left
		if f != nil {
			_ = h.svc.Delete(context.WithoutCancel(ctx), f.ID)
		}
		api.Fail(w, r, err)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(uploadErr(err))
			return
		}
		switch part.FormName() {
		case "metadata":
			if md, err = readMetadata(part); err != nil {
				fail(err)
				return
			}
			lateMeta = f != nil
		case "file":
			if f != nil {
				continue
			}
			meta := map[string]any{}
			if md != nil {
				meta["data"] = md
			}
			nf, err := h.svc.store(ctx, u.ID, part.FileName(), "", part, meta, process)
			if err != nil {
				api.Fail(w, r, uploadErr(err))
				return
			}
			f = &nf
		}
	}
	if f == nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", `missing multipart field "file"`))
		return
	}
	if lateMeta && md != nil {
		if err := h.svc.setMeta(ctx, f, "data", md); err != nil {
			fail(err)
			return
		}
	}
	if process {
		h.svc.enqueue(f.ID)
	}
	api.JSON(w, http.StatusOK, JSON(*f))
}

func readMetadata(p *multipart.Part) (any, error) {
	b, err := io.ReadAll(io.LimitReader(p, 1<<20))
	if err != nil {
		return nil, uploadErr(err)
	}
	if s := strings.TrimSpace(string(b)); s == "" || s == "null" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, api.Errf(http.StatusBadRequest, "bad_json", "metadata is not valid JSON")
	}
	return v, nil
}

func uploadErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.Is(err, ErrTooLarge) || errors.As(err, &mbe) {
		return api.Errf(http.StatusRequestEntityTooLarge, "too_large", "File exceeds the upload size limit")
	}
	if strings.Contains(err.Error(), "multipart") {
		return api.Errf(http.StatusBadRequest, "bad_request", "malformed multipart body")
	}
	return err
}

// setMeta sets one top-level meta key.
func (s *Service) setMeta(ctx context.Context, f *File, key string, v any) error {
	b, err := json.Marshal(map[string]any{key: v})
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE file SET meta = meta || $2, updated_at = now() WHERE id = $1`, f.ID, b); err != nil {
		return fmt.Errorf("update file meta: %w", err)
	}
	f.Meta[key] = v
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	owner := u.ID
	if u.IsAdmin() && r.URL.Query().Get("all") == "true" {
		owner = ""
	}
	fs, err := h.svc.List(r.Context(), owner)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out := make([]map[string]any, len(fs))
	for i, f := range fs {
		out[i] = JSON(f)
	}
	api.JSON(w, http.StatusOK, out)
}

// readable loads the path's file when the user may read it; otherwise it
// answers 404 (existence is not revealed) and returns false.
func (h *handler) readable(w http.ResponseWriter, r *http.Request) (File, bool) {
	id := r.PathValue("id")
	ok, err := h.svc.CanRead(r.Context(), auth.FromContext(r.Context()), id)
	if err != nil {
		api.Fail(w, r, err)
		return File{}, false
	}
	if !ok {
		api.Fail(w, r, errNotFound)
		return File{}, false
	}
	f, err := h.svc.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		err = errNotFound
	}
	if err != nil {
		api.Fail(w, r, err)
		return File{}, false
	}
	return f, true
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	if f, ok := h.readable(w, r); ok {
		api.JSON(w, http.StatusOK, JSON(f))
	}
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	if f, ok := h.readable(w, r); ok {
		api.JSON(w, http.StatusOK, map[string]any{"status": f.Status})
	}
}

func (h *handler) text(w http.ResponseWriter, r *http.Request) {
	f, ok := h.readable(w, r)
	if !ok {
		return
	}
	t, err := h.svc.Text(r.Context(), f.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"content": t})
}

func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	f, ok := h.readable(w, r)
	if !ok {
		return
	}
	sheet, _ := strconv.Atoi(r.URL.Query().Get("sheet"))
	p, err := h.svc.Preview(r.Context(), f.ID, sheet)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, p)
}

func (h *handler) content(w http.ResponseWriter, r *http.Request) {
	f, ok := h.readable(w, r)
	if !ok {
		return
	}
	rc, f, err := h.svc.Open(r.Context(), f.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer rc.Close()
	hd := w.Header()
	hd.Set("Content-Type", f.ContentType)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "private, no-cache")
	active := activeContent(f.ContentType)
	if active {
		hd.Set("Content-Security-Policy", "sandbox")
	}
	disp := "attachment"
	if r.URL.Query().Get("attachment") != "true" && !active && inlineType(f.ContentType) {
		disp = "inline"
	}
	hd.Set("Content-Disposition", ContentDisposition(disp, f.Filename))
	http.ServeContent(w, r, f.Filename, f.UpdatedAt, rc)
}

func inlineType(ct string) bool {
	return strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "application/pdf") || strings.HasPrefix(ct, "text/plain")
}

// activeContent can run script when rendered by a browser.
func activeContent(ct string) bool {
	ct = strings.ToLower(ct)
	for _, p := range []string{"text/html", "image/svg", "application/xhtml", "text/xml", "application/xml", "text/javascript", "application/javascript"} {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

// ContentDisposition builds an RFC 6266 header with an ASCII fallback and a UTF-8 filename*.
func ContentDisposition(disp, name string) string {
	var ascii, enc strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			ascii.WriteByte('_')
		} else {
			ascii.WriteRune(r)
		}
	}
	for _, b := range []byte(name) {
		if b < 0x80 && (b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("!#$&+-.^_`|~", b) >= 0) {
			enc.WriteByte(b)
		} else {
			fmt.Fprintf(&enc, "%%%02X", b)
		}
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disp, ascii.String(), enc.String())
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := auth.FromContext(ctx)
	f, ok := h.readable(w, r)
	if !ok {
		return
	}
	if f.UserID != u.ID && !u.IsAdmin() {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Only the owner can delete this file"))
		return
	}
	if err := h.svc.Delete(ctx, f.ID); err != nil && !errors.Is(err, ErrNotFound) {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"message": "File deleted"})
}
