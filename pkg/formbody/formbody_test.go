package formbody

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type uploadFile struct {
	field       string
	name        string
	contentType string
	content     []byte
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
	return withReplacer(req)
}

func formRequest(values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return withReplacer(req)
}

func withReplacer(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
}

func TestEnsureParsesURLEncodedForm(t *testing.T) {
	state, err := Ensure(formRequest(url.Values{"username": {"alice"}, "notes": {"hi"}}), Limits{MaxPartSize: 1024, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := state.Values.Get("username"); got != "alice" {
		t.Errorf("username = %q", got)
	}
	if got := state.Values.Get("notes"); got != "hi" {
		t.Errorf("notes = %q", got)
	}
	if len(state.Parts) != 0 {
		t.Errorf("parts = %d, want 0", len(state.Parts))
	}
}

func TestEnsureBuffersMultipartParts(t *testing.T) {
	tempDir := t.TempDir()
	req := multipartRequest(t,
		map[string]string{"username": "alice"},
		[]uploadFile{
			{field: "photo", name: "photo.png", contentType: "image/png", content: []byte("png-bytes")},
			{field: "doc", name: "doc.pdf", contentType: "application/pdf", content: []byte("pdf-bytes")},
		},
	)

	state, err := Ensure(req, Limits{MaxPartSize: 1024, TempDir: tempDir})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := state.Values.Get("username"); got != "alice" {
		t.Errorf("username = %q", got)
	}
	if len(state.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(state.Parts))
	}

	first := state.Parts[0]
	if first.FieldName != "photo" || first.Filename != "photo.png" || first.MimeType != "image/png" {
		t.Errorf("unexpected part: %+v", first)
	}
	if first.Size != int64(len("png-bytes")) {
		t.Errorf("size = %d", first.Size)
	}
	if filepath.Dir(first.TempPath) != tempDir {
		t.Errorf("temp path %q not in %q", first.TempPath, tempDir)
	}

	content, err := os.ReadFile(first.TempPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "png-bytes" {
		t.Errorf("buffered content = %q", content)
	}

	state.Cleanup()
	for _, part := range state.Parts {
		if _, err := os.Stat(part.TempPath); !os.IsNotExist(err) {
			t.Errorf("temp file %q survived cleanup", part.TempPath)
		}
	}
}

func TestEnsureReturnsExistingState(t *testing.T) {
	req := multipartRequest(t, map[string]string{"username": "alice"}, nil)

	first, err := Ensure(req, Limits{MaxPartSize: 1024, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	second, err := Ensure(req, Limits{MaxPartSize: 1024, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure (second): %v", err)
	}
	if first != second {
		t.Fatal("Ensure returned a different state on the second call")
	}
	if second != FromRequest(req) {
		t.Fatal("state is not attached to the request")
	}
}

func TestEnsureRejectsOversizePart(t *testing.T) {
	req := multipartRequest(t, nil, []uploadFile{
		{field: "photo", name: "big.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("x"), 64)},
	})

	state, err := Ensure(req, Limits{MaxPartSize: 8, TempDir: t.TempDir()})
	if err == nil {
		state.Cleanup()
		t.Fatal("Ensure succeeded, want ErrTooLarge")
	}

	var handlerErr caddyhttp.HandlerError
	if !errors.As(err, &handlerErr) {
		t.Fatalf("error = %v, want HandlerError", err)
	}
	if handlerErr.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", handlerErr.StatusCode)
	}
}

func TestEnsureSanitizesUnsniffablePart(t *testing.T) {
	req := multipartRequest(t, nil, []uploadFile{
		{field: "blob", name: "unknown", contentType: "", content: []byte{0x00, 0x01, 0x02}},
	})

	state, err := Ensure(req, Limits{MaxPartSize: 1024, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer state.Cleanup()

	if len(state.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(state.Parts))
	}
	if state.Parts[0].MimeType != "application/octet-stream" {
		t.Errorf("mime = %q", state.Parts[0].MimeType)
	}
}

func TestPublishFields(t *testing.T) {
	req := withReplacer(httptest.NewRequest(http.MethodPost, "/upload", nil))

	PublishFields(req, url.Values{"username": {"alice"}, "empty": {}})
	repl := Replacer(req)
	if got := repl.ReplaceAll("{http.vars.form.username}", ""); got != "alice" {
		t.Errorf("username placeholder = %q", got)
	}
	if got := repl.ReplaceAll("{http.vars.form.empty}", "fallback"); got != "fallback" {
		t.Errorf("empty placeholder = %q, want fallback", got)
	}
	if got := repl.ReplaceAll("{http.vars.form.missing}", "fallback"); got != "fallback" {
		t.Errorf("missing placeholder = %q, want fallback", got)
	}
}

func TestEnsureAttachesFormToRequest(t *testing.T) {
	req := multipartRequest(t, map[string]string{"username": "alice", "notes": "hi"}, nil)
	req.URL.RawQuery = "username=query&topic=uploads"

	if _, err := Ensure(req, Limits{MaxPartSize: 1024, TempDir: t.TempDir()}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := req.PostForm.Get("username"); got != "alice" {
		t.Errorf("PostForm.username = %q, want the body value", got)
	}
	if got := req.Form.Get("username"); got != "alice" {
		t.Errorf("Form.username = %q, want the body value ahead of the query", got)
	}
	if got := req.Form.Get("topic"); got != "uploads" {
		t.Errorf("Form.topic = %q, want the query value merged in", got)
	}
}
