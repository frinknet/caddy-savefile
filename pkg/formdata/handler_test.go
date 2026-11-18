package formdata

import (
	"bytes"
	"context"
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
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/formbody"
	"github.com/frinknet/caddy-savefile/pkg/paths"
)

type nextHandler struct {
	called bool
}

func (n *nextHandler) ServeHTTP(http.ResponseWriter, *http.Request) error {
	n.called = true
	return nil
}

func withReplacer(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
}

func multipartRequest(t *testing.T, fields map[string]string) *http.Request {
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

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="photo"; filename="photo.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return withReplacer(req)
}

func TestFormDataPublishesURLEncodedFields(t *testing.T) {
	body := url.Values{"username": {"alice"}, "notes": {"hello"}}.Encode()
	req := withReplacer(httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	handler := FormData{MaxMemory: config.DefaultMaxSize, TempDir: t.TempDir()}
	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if !next.called {
		t.Error("next handler was not called")
	}

	repl := req.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	if got := repl.ReplaceAll("{http.vars.form.username}", ""); got != "alice" {
		t.Errorf("username = %q", got)
	}
	if got := repl.ReplaceAll("{http.vars.form.notes}", ""); got != "hello" {
		t.Errorf("notes = %q", got)
	}
}

func TestFormDataPublishesMultipartFields(t *testing.T) {
	req := multipartRequest(t, map[string]string{"username": "bob", "notes": "hi"})

	handler := FormData{MaxMemory: config.DefaultMaxSize, TempDir: t.TempDir()}
	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	repl := req.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	if got := repl.ReplaceAll("{http.vars.form.username}", ""); got != "bob" {
		t.Errorf("username = %q", got)
	}
}

func TestFormDataIgnoresNonUploadMethods(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/upload", nil)

	handler := FormData{}
	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}
	if !next.called {
		t.Error("next handler was not called")
	}
}

func TestFormDataProvisionUsesGlobalConfig(t *testing.T) {
	original := config.Global()
	t.Cleanup(func() { config.Store(original) })

	tempDir := filepath.Join(t.TempDir(), "scratch")
	config.Store(config.Config{
		RootDir:    t.TempDir(),
		MaxSize:    2048,
		MaxParts:   5,
		TempDir:    tempDir,
		VerifyMime: paths.VerifyMimeOff,
	})

	handler := new(FormData)
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	if handler.MaxMemory != 2048 || handler.TempDir != tempDir {
		t.Errorf("provisioned = %+v", handler)
	}
	if handler.MaxParts != 5 || handler.VerifyMime != paths.VerifyMimeOff {
		t.Errorf("provisioned = %+v", handler)
	}
	if info, err := os.Stat(tempDir); err != nil || !info.IsDir() {
		t.Errorf("provisioning did not create temp_dir: %v", err)
	}
}

func TestFormDataUnmarshalCaddyfile(t *testing.T) {
	if err := new(FormData).UnmarshalCaddyfile(caddyfile.NewTestDispenser("formdata")); err != nil {
		t.Errorf("UnmarshalCaddyfile: %v", err)
	}
	if err := new(FormData).UnmarshalCaddyfile(caddyfile.NewTestDispenser("formdata extra")); err == nil {
		t.Error("unexpected argument was accepted")
	}
}

func TestFormDataSharesStateWithSavefile(t *testing.T) {
	req := multipartRequest(t, map[string]string{"username": "carol"})

	handler := FormData{MaxMemory: config.DefaultMaxSize, TempDir: t.TempDir()}
	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatal(err)
	}

	// Streaming the body leaves MultipartForm as the empty sentinel; buffering
	// it would have populated File.
	if req.MultipartForm != nil && len(req.MultipartForm.File) != 0 {
		t.Errorf("multipart body was buffered instead of streamed: %+v", req.MultipartForm)
	}

	state := formbody.FromRequest(req)
	if state == nil {
		t.Fatal("no shared form state attached to the request")
	}
	if len(state.Parts) != 1 {
		t.Errorf("parts = %d, want 1", len(state.Parts))
	}
	if got := req.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer).ReplaceAll("{http.vars.form.username}", ""); got != "carol" {
		t.Errorf("username = %q", got)
	}
}
