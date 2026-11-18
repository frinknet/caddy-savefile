package formbody

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func jsonRequest(contentType, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	return withReplacer(req)
}

func ensureJSON(t *testing.T, req *http.Request) (*FormState, error) {
	t.Helper()

	state, err := Ensure(req, 4096, t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return state, state.IngestJSON(req)
}

func TestIngestJSONFlattens(t *testing.T) {
	req := jsonRequest("application/json", `{
		"username": "alice",
		"count": 1000000000,
		"active": true,
		"missing": null,
		"address": {"city": "berlin", "geo": {"lat": "52.5"}},
		"tags": ["a", "b"],
		"items": [{"name": "first"}, {"name": "second"}]
	}`)

	state, err := ensureJSON(t, req)
	if err != nil {
		t.Fatalf("IngestJSON: %v", err)
	}

	for _, test := range []struct {
		key  string
		want string
	}{
		{"username", "alice"},
		{"count", "1000000000"},
		{"active", "true"},
		{"missing", ""},
		{"address.city", "berlin"},
		{"address.geo.lat", "52.5"},
		{"items.0.name", "first"},
		{"items.1.name", "second"},
	} {
		if got := state.Values.Get(test.key); got != test.want {
			t.Errorf("Values.Get(%q) = %q, want %q", test.key, got, test.want)
		}
	}

	if got := state.Values["tags"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("tags = %v, want [a b]", got)
	}
	if got := req.Form.Get("username"); got != "alice" {
		t.Errorf("Form.username = %q, want the folded value", got)
	}
	if !state.Consumed() {
		t.Error("JSON body did not mark the state consumed")
	}
}

func TestIngestJSONAcceptsSubtypes(t *testing.T) {
	req := jsonRequest("application/vnd.api+json; charset=utf-8", `{"username": "bob"}`)

	state, err := ensureJSON(t, req)
	if err != nil {
		t.Fatalf("IngestJSON: %v", err)
	}
	if got := state.Values.Get("username"); got != "bob" {
		t.Errorf("username = %q", got)
	}
}

func TestIngestJSONRejectsBadPayload(t *testing.T) {
	req := jsonRequest("application/json", `{"username": `)

	if _, err := ensureJSON(t, req); err == nil {
		t.Fatal("malformed JSON was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusBadRequest {
		t.Errorf("error = %v, want 400", err)
	}
}

func TestIngestJSONRejectsOversizeBody(t *testing.T) {
	req := jsonRequest("application/json", `{"username": "`+strings.Repeat("x", 8192)+`"}`)

	if _, err := ensureJSON(t, req); err == nil {
		t.Fatal("oversize JSON was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}
}

func TestIngestJSONIgnoresOtherMediaTypes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader("<!DOCTYPE html>"))
	req.Header.Set("Content-Type", "text/html")

	state, err := ensureJSON(t, req)
	if err != nil {
		t.Fatalf("IngestJSON: %v", err)
	}
	if len(state.Values) != 0 {
		t.Errorf("values = %v, want none", state.Values)
	}
	if state.Consumed() {
		t.Error("a non-JSON body was marked consumed")
	}
}

func TestIngestJSONSkipsConsumedState(t *testing.T) {
	req := jsonRequest("application/json", `{"username": "alice"}`)

	state := &FormState{Values: url.Values{}, maxSize: 4096, tempDir: t.TempDir()}
	state.consumed = true

	if err := state.IngestJSON(req); err != nil {
		t.Fatalf("IngestJSON: %v", err)
	}
	if len(state.Values) != 0 {
		t.Errorf("values = %v, want none for an already consumed body", state.Values)
	}
}

func errorStatus(err error) (int, bool) {
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
