package formdata

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"github.com/frinknet/caddy-savefile/pkg/config"
)

func TestFormDataPublishesJSONFields(t *testing.T) {
	body := `{"username":"dave","address":{"city":"berlin"},"tags":["a","b"]}`
	req := withReplacer(httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")

	handler := FormData{MaxMemory: config.DefaultMaxSize, TempDir: t.TempDir()}
	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if !next.called {
		t.Error("next handler was not called")
	}

	repl := req.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	for placeholder, want := range map[string]string{
		"{http.vars.form.username}":     "dave",
		"{http.vars.form.address.city}": "berlin",
		"{http.vars.form.tags}":         "a",
	} {
		if got := repl.ReplaceAll(placeholder, ""); got != want {
			t.Errorf("%s = %q, want %q", placeholder, got, want)
		}
	}

	if got := req.Form.Get("username"); got != "dave" {
		t.Errorf("Form.username = %q", got)
	}
	if got := req.Form["tags"]; len(got) != 2 {
		t.Errorf("Form[tags] = %v, want both values", got)
	}
}

func TestFormDataRejectsMalformedJSON(t *testing.T) {
	req := withReplacer(httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{"username":`)))
	req.Header.Set("Content-Type", "application/json")

	handler := FormData{MaxMemory: config.DefaultMaxSize, TempDir: t.TempDir()}
	var next nextHandler
	if err := handler.ServeHTTP(httptest.NewRecorder(), req, &next); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}
