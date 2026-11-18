package savefile

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/formbody"
	"github.com/frinknet/caddy-savefile/pkg/formdata"
)

type uploadFile struct {
	field       string
	name        string
	contentType string
	content     []byte
}

type nextHandler struct {
	called bool
}

func (n *nextHandler) ServeHTTP(http.ResponseWriter, *http.Request) error {
	n.called = true
	return nil
}

func multipartRequest(t *testing.T, fields map[string]string, files []uploadFile) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := writer.WriteField(key, fields[key]); err != nil {
			t.Fatal(err)
		}
	}

	for _, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+file.field+`"; filename="`+file.name+`"`)
		header.Set("Content-Type", file.contentType)

		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
}

func newHandler(rootDir, filter, destination string, maxSize int64) (formdata.FormData, SaveFile) {
	tempDir := filepath.Join(rootDir, "..", "scratch")
	form := formdata.FormData{MaxMemory: maxSize, TempDir: tempDir}
	handler := SaveFile{
		MimeFilter:  filter,
		Destination: destination,
		RootDir:     rootDir,
		TempDir:     tempDir,
		MaxSize:     maxSize,
	}
	return form, handler
}

func TestSaveFileRoutesMatchedParts(t *testing.T) {
	root := t.TempDir()
	form, handler := newHandler(root, "image/*", "images/{uuid}_{filename}", config.DefaultMaxSize)

	req := multipartRequest(t,
		map[string]string{"username": "alice"},
		[]uploadFile{
			{field: "photo", name: "photo.png", contentType: "image/png", content: []byte("png-bytes")},
			{field: "doc", name: "doc.pdf", contentType: "application/pdf", content: []byte("pdf-bytes")},
		},
	)

	var next nextHandler
	if err := form.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatalf("formdata: %v", err)
	}
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatalf("savefile: %v", err)
	}
	if !next.called {
		t.Error("savefile did not call the next handler")
	}

	entries, err := os.ReadDir(filepath.Join(root, "images"))
	if err != nil {
		t.Fatalf("reading images dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("saved %d files, want 1", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), "_photo.png") {
		t.Errorf("saved filename = %q", entries[0].Name())
	}
	if _, err := os.Stat(filepath.Join(root, "docs")); !os.IsNotExist(err) {
		t.Error("unmatched application/pdf part was written to disk")
	}

	content, err := os.ReadFile(filepath.Join(root, "images", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "png-bytes" {
		t.Errorf("saved content = %q", content)
	}

	// Saved-file metadata deliberately stays out of the vars tree; form fields
	// are the only thing published there.
	if got := req.Form.Get("username"); got != "alice" {
		t.Errorf("Form.username = %q", got)
	}
	if got := req.PostForm.Get("username"); got != "alice" {
		t.Errorf("PostForm.username = %q", got)
	}

	repl := req.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	if got := repl.ReplaceAll("{http.vars.form.username}", ""); got != "alice" {
		t.Errorf("form placeholder = %q", got)
	}
	if got := repl.ReplaceAll("{http.vars.savefile.filename}", "unset"); got != "unset" {
		t.Errorf("savefile placeholder = %q, want it absent", got)
	}
}

func TestSaveFileKeepsFileAndFormSeparateAcrossRules(t *testing.T) {
	root := t.TempDir()
	form, images := newHandler(root, "image/*", "images/{filename}", config.DefaultMaxSize)
	_, documents := newHandler(root, "application/pdf", "docs/{filename}", config.DefaultMaxSize)

	req := multipartRequest(t, nil, []uploadFile{
		{field: "photo", name: "photo.png", contentType: "image/png", content: []byte("png")},
		{field: "doc", name: "doc.pdf", contentType: "application/pdf", content: []byte("pdf")},
		{field: "clip", name: "clip.mp4", contentType: "video/mp4", content: []byte("mp4")},
	})

	var next nextHandler
	if err := form.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if err := images.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if err := documents.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}

	state := formbody.FromRequest(req)
	if len(state.Files) != 2 {
		t.Fatalf("saved %d files, want 2", len(state.Files))
	}
	for _, file := range state.Files {
		switch file.OriginalFilename {
		case "photo.png", "doc.pdf":
		default:
			t.Errorf("unexpected saved file %q", file.OriginalFilename)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "images", "photo.png")); err != nil {
		t.Errorf("image not saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "doc.pdf")); err != nil {
		t.Errorf("document not saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "images", "clip.mp4")); !os.IsNotExist(err) {
		t.Error("unhandled MIME type was written to disk")
	}
}

func TestSaveFileSanitizesClientFilename(t *testing.T) {
	root := t.TempDir()
	form, handler := newHandler(root, "*", "incoming/{filename}", config.DefaultMaxSize)

	req := multipartRequest(t, nil, []uploadFile{
		{field: "file", name: "../../../../etc/passwd", contentType: "text/plain", content: []byte("nope")},
	})

	var next nextHandler
	if err := form.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}

	saved := filepath.Join(root, "incoming", "passwd")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("expected sanitized file at %s: %v", saved, err)
	}
	for _, escaped := range []string{
		filepath.Join(filepath.Dir(root), "passwd"),
		filepath.Join(filepath.Dir(root), "incoming", "passwd"),
	} {
		if _, err := os.Stat(escaped); err == nil {
			t.Errorf("filename escaped root_dir to %s", escaped)
		}
	}
}

func TestSaveFilePayloadTooLarge(t *testing.T) {
	root := t.TempDir()
	form, handler := newHandler(root, "*", "files/{filename}", 8)

	req := multipartRequest(t, nil, []uploadFile{
		{field: "file", name: "big.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("x"), 64)},
	})

	var next nextHandler
	err := form.ServeHTTP(httptest.NewRecorder(), req, &next)
	if err == nil {
		err = handler.ServeHTTP(httptest.NewRecorder(), req, &next)
	}
	if err == nil {
		t.Fatal("oversize upload succeeded")
	}

	status, ok := handlerErrorStatus(err)
	if !ok {
		t.Fatalf("error = %v, want HandlerError", err)
	}
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", status)
	}

	entries, err := os.ReadDir(filepath.Join(root, "files"))
	if err == nil && len(entries) != 0 {
		t.Errorf("partial file left on disk: %v", entries)
	}
}

func TestSaveFileIgnoresNonUploadMethods(t *testing.T) {
	root := t.TempDir()
	_, handler := newHandler(root, "*", "files/{filename}", config.DefaultMaxSize)

	req := httptest.NewRequest(http.MethodGet, "/upload", nil)
	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if !next.called {
		t.Error("next handler was not called")
	}
}

func TestValidate(t *testing.T) {
	if err := (SaveFile{Destination: "x"}).Validate(); err == nil {
		t.Error("missing MIME filter passed validation")
	}
	if err := (SaveFile{MimeFilter: "*"}).Validate(); err == nil {
		t.Error("missing destination passed validation")
	}
	if err := (SaveFile{MimeFilter: "*", Destination: "x"}).Validate(); err != nil {
		t.Errorf("valid handler failed validation: %v", err)
	}
}

func TestProvisionUsesGlobalConfig(t *testing.T) {
	original := config.Global()
	t.Cleanup(func() { config.Store(original) })

	config.Store(config.Config{RootDir: "/global/root", MaxSize: 4242, TempDir: "/global/tmp"})

	handler := new(SaveFile)
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	if handler.RootDir != "/global/root" || handler.MaxSize != 4242 || handler.TempDir != "/global/tmp" {
		t.Errorf("provisioned = %+v", handler)
	}

	explicit := &SaveFile{RootDir: "/explicit", MaxSize: 1, TempDir: "/explicit/tmp"}
	if err := explicit.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	if explicit.RootDir != "/explicit" || explicit.MaxSize != 1 || explicit.TempDir != "/explicit/tmp" {
		t.Errorf("explicit config overwritten: %+v", explicit)
	}
}

func TestUnmarshalCaddyfile(t *testing.T) {
	dispenser := caddyfile.NewTestDispenser("savefile image/* /var/uploads/images/{uuid}_{filename}")

	handler := new(SaveFile)
	if err := handler.UnmarshalCaddyfile(dispenser); err != nil {
		t.Fatalf("UnmarshalCaddyfile: %v", err)
	}
	if handler.MimeFilter != "image/*" {
		t.Errorf("MimeFilter = %q", handler.MimeFilter)
	}
	if handler.Destination != "/var/uploads/images/{uuid}_{filename}" {
		t.Errorf("Destination = %q", handler.Destination)
	}

	if err := new(SaveFile).UnmarshalCaddyfile(caddyfile.NewTestDispenser("savefile image/*")); err == nil {
		t.Error("missing destination was accepted")
	}
	if err := new(SaveFile).UnmarshalCaddyfile(caddyfile.NewTestDispenser("savefile a b c")); err == nil {
		t.Error("extra arguments were accepted")
	}
}

func TestSaveFileClaimsCanonicalizedUUIDShorthand(t *testing.T) {
	root := t.TempDir()
	form, handler := newHandler(root, "*", "images/{http.request.uuid}_{filename}", config.DefaultMaxSize)

	req := multipartRequest(t, nil, []uploadFile{
		{field: "file", name: "note.txt", contentType: "text/plain", content: []byte("x")},
	})

	var next nextHandler
	if err := form.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("saved %d files, want 1", len(entries))
	}

	canonicalUUID := regexp.MustCompile("^" + uuidV4Pattern + `_note\.txt$`)
	if !canonicalUUID.MatchString(entries[0].Name()) {
		t.Errorf("saved filename = %q, want a v4 UUID prefix", entries[0].Name())
	}
}

func TestSaveFileGivesEachPartItsOwnName(t *testing.T) {
	root := t.TempDir()
	form, handler := newHandler(root, "*", "images/{uuid}_{filename}", config.DefaultMaxSize)

	req := multipartRequest(t, nil, []uploadFile{
		{field: "first", name: "same.txt", contentType: "text/plain", content: []byte("one")},
		{field: "second", name: "same.txt", contentType: "text/plain", content: []byte("two")},
	})

	var next nextHandler
	if err := form.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("saved %d files, want 2", len(entries))
	}
	if entries[0].Name() == entries[1].Name() {
		t.Errorf("both parts were written to %q", entries[0].Name())
	}
}

const uuidV4Pattern = `[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`

func rawRequest(t *testing.T, target, contentType, body string) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	return req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
}

func TestSaveFileIngestsRawBody(t *testing.T) {
	root := t.TempDir()
	_, handler := newHandler(root, "image/*", "images/{uuid}_{filename}", config.DefaultMaxSize)

	req := rawRequest(t, "/upload", "image/png", "raw-png-bytes")

	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatalf("savefile: %v", err)
	}
	if !next.called {
		t.Error("next handler was not called")
	}

	entries, err := os.ReadDir(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("saved %d files, want 1", len(entries))
	}
	if !regexp.MustCompile("^" + uuidV4Pattern + `_upload\.png$`).MatchString(entries[0].Name()) {
		t.Errorf("saved filename = %q, want <uuid>_upload.png", entries[0].Name())
	}

	content, err := os.ReadFile(filepath.Join(root, "images", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "raw-png-bytes" {
		t.Errorf("saved content = %q", content)
	}
}

func TestSaveFileDiscardsUnmatchedRawBody(t *testing.T) {
	root := t.TempDir()
	_, handler := newHandler(root, "application/pdf", "docs/{filename}", config.DefaultMaxSize)

	req := rawRequest(t, "/upload", "image/png", "raw-png-bytes")

	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatalf("savefile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs")); !os.IsNotExist(err) {
		t.Error("a raw body whose MIME type matched no rule was written to disk")
	}
}

func TestSaveFileLeavesJSONToFormdata(t *testing.T) {
	root := t.TempDir()
	form, handler := newHandler(root, "*", "files/{filename}", config.DefaultMaxSize)

	req := rawRequest(t, "/upload", "application/json", `{"username":"erin"}`)

	var next nextHandler
	if err := form.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "files")); !os.IsNotExist(err) {
		t.Error("a JSON body was stored as a file instead of being folded into fields")
	}
	if got := req.Form.Get("username"); got != "erin" {
		t.Errorf("Form.username = %q", got)
	}
}

func handlerErrorStatus(err error) (int, bool) {
	var pointer *caddyhttp.HandlerError
	if errors.As(err, &pointer) {
		return pointer.StatusCode, true
	}
	var value caddyhttp.HandlerError
	if errors.As(err, &value) {
		return value.StatusCode, true
	}
	return 0, false
}
