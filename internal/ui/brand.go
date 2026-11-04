package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/apply"
)

// maxBrandUpload caps one uploaded brand image.
const maxBrandUpload = 2 << 20

// uploadTypes are the image types the admin UI may upload, by extension.
var uploadTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp",
	".ico": "image/x-icon", ".svg": "image/svg+xml",
}

// adminRoutes are the brand editor's endpoints; the brand itself is saved
// through the settings API (key ui.brand).
func (u *UI) adminRoutes(s *api.Server, admin func(http.HandlerFunc) http.Handler) {
	s.Handle("GET /api/v1/admin/brand/assets", admin(u.listAssets))
	s.Handle("POST /api/v1/admin/brand/assets", admin(u.uploadAsset))
	s.Handle("DELETE /api/v1/admin/brand/assets/{file}", admin(u.deleteAsset))
	s.Handle("GET /api/v1/admin/brand/config", admin(u.configBrand))
}

type assetOut struct {
	File string `json:"file"`
	URL  string `json:"url"`
	Dir  string `json:"dir"` // "uploads" (deletable) or "config"
	Size int64  `json:"size"`
}

func (u *UI) listAssets(w http.ResponseWriter, r *http.Request) {
	out := []assetOut{}
	for i, root := range u.assets.roots() {
		dir := "config"
		if i == 0 && root == u.assets.upload {
			dir = "uploads"
		}
		entries, err := os.ReadDir(root)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			api.Fail(w, r, err)
			return
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() || !validName(e.Name()) {
				continue
			}
			out = append(out, assetOut{File: e.Name(), URL: brandPrefix + e.Name(), Dir: dir, Size: info.Size()})
		}
	}
	api.JSON(w, http.StatusOK, out)
}

func (u *UI) uploadAsset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBrandUpload+64<<10) // + multipart framing
	f, hdr, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			api.Fail(w, r, api.Errf(http.StatusRequestEntityTooLarge, "too_large", "Images are limited to 2 MB"))
			return
		}
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", "expected multipart/form-data with a file field"))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBrandUpload+1))
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", "could not read the upload"))
		return
	}
	name, err := brandFileName(r.FormValue("slot"), hdr.Filename, data)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := writeAsset(u.assets.upload, name, data); err != nil {
		api.Fail(w, r, err)
		return
	}
	u.expire()
	api.JSON(w, http.StatusOK, assetOut{File: name, URL: brandPrefix + name, Dir: "uploads", Size: int64(len(data))})
}

// brandFileName checks an uploaded image and names it <slot or file
// name>-<sha8>.<ext>. The content must be the image type its extension says.
func brandFileName(slot, filename string, data []byte) (string, error) {
	if len(data) > maxBrandUpload {
		return "", api.Errf(http.StatusRequestEntityTooLarge, "too_large", "Images are limited to 2 MB")
	}
	if len(data) == 0 {
		return "", api.Errf(http.StatusBadRequest, "empty", "The file is empty")
	}
	ext := strings.ToLower(path.Ext(filename))
	want, ok := uploadTypes[ext]
	if !ok {
		return "", api.Errf(http.StatusBadRequest, "bad_type", "Upload a PNG, JPEG, WebP, SVG or ICO image")
	}
	if got := sniffImage(data); got != want {
		return "", api.Errf(http.StatusBadRequest, "bad_type", "The file content is not a "+strings.TrimPrefix(ext, ".")+" image")
	}
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	base := slug(slot)
	if base == "" {
		base = slug(strings.TrimSuffix(path.Base(strings.ReplaceAll(filename, `\`, "/")), path.Ext(filename)))
	}
	if base == "" {
		base = "image"
	}
	sum := sha256.Sum256(data)
	return base + "-" + hex.EncodeToString(sum[:])[:8] + ext, nil
}

// sniffImage is the MIME type of data: the stdlib sniffer for raster
// formats, an <svg> root element for SVG.
func sniffImage(data []byte) string {
	t := http.DetectContentType(data)
	if strings.HasPrefix(t, "image/") && t != "image/svg+xml" {
		return t
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return t
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local == "svg" {
				return "image/svg+xml"
			}
			return t
		}
	}
}

var notSlug = regexp.MustCompile(`[^a-z0-9_-]+`)

// slug keeps a name to [a-z0-9_-], at most 40 characters.
func slug(s string) string {
	s = strings.Trim(notSlug.ReplaceAllString(strings.ToLower(s), "-"), "-_")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-_")
	}
	return s
}

// writeAsset stores data as dir/name, creating dir on first use.
func writeAsset(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return api.Errf(http.StatusInternalServerError, "brand_dir", "cannot create the brand uploads directory: "+err.Error())
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return api.Errf(http.StatusInternalServerError, "brand_dir", "the brand uploads directory is not writable: "+err.Error())
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// deleteAsset removes an uploaded image; files in the config dir are read-only.
func (u *UI) deleteAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	root, err := os.OpenRoot(u.assets.upload)
	if err == nil {
		defer root.Close()
		var st fs.FileInfo
		if st, err = root.Lstat(name); err == nil && validName(name) && st.Mode().IsRegular() {
			err = root.Remove(name)
		} else if err == nil {
			err = fs.ErrNotExist
		}
	}
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "No uploaded image "+name+" (images in the config brand directory cannot be deleted here)"))
		return
	}
	u.expire()
	w.WriteHeader(http.StatusNoContent)
}

// configBrand returns the brand section of the config file, for "reset to
// the config file".
// ponytail: ${VAR} references in the brand section are returned unexpanded;
// run the brand through apply.Parse if a deployment ever templates it.
func (u *UI) configBrand(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Brand *apply.Brand `yaml:"brand"`
	}
	data, err := os.ReadFile(u.configFile)
	if u.configFile == "" || err != nil || yaml.Unmarshal(data, &c) != nil || c.Brand == nil {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "The config file has no brand section"))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"brand": c.Brand})
}
