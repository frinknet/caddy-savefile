package formbody

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestEnsureRejectsOversizeURLEncoded(t *testing.T) {
	req := formRequest(map[string][]string{"notes": {strings.Repeat("x", 512)}})

	if _, err := Ensure(req, Limits{MaxPartSize: 64, TempDir: t.TempDir()}); err == nil {
		t.Fatal("oversize urlencoded body was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}
}

func TestEnsureRejectsOversizeDeclaredURLEncoded(t *testing.T) {
	req := formRequest(map[string][]string{"notes": {"small"}})
	req.ContentLength = 1 << 20

	if _, err := Ensure(req, Limits{MaxPartSize: 64, TempDir: t.TempDir()}); err == nil {
		t.Fatal("declared oversize body was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}
}

func TestEnsureCapsFilePartCount(t *testing.T) {
	tempDir := t.TempDir()
	req := multipartRequest(t, nil, []uploadFile{
		{field: "a", name: "a.png", contentType: "image/png", content: []byte("a")},
		{field: "b", name: "b.png", contentType: "image/png", content: []byte("b")},
		{field: "c", name: "c.png", contentType: "image/png", content: []byte("c")},
	})

	if _, err := Ensure(req, Limits{MaxPartSize: 1024, MaxParts: 2, TempDir: tempDir}); err == nil {
		t.Fatal("part count over the limit was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}

	entries, readErr := os.ReadDir(tempDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("temp files survived a rejected request: %v", entries)
	}
}

func TestEnsureCapsFieldPartCount(t *testing.T) {
	req := multipartRequest(t, map[string]string{"a": "1", "b": "2", "c": "3"}, nil)

	if _, err := Ensure(req, Limits{MaxPartSize: 1024, MaxParts: 2, TempDir: t.TempDir()}); err == nil {
		t.Fatal("field parts were not counted against the part cap")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}
}

func TestEnsureRejectsDeclaredOversizeMultipart(t *testing.T) {
	req := multipartRequest(t, nil, []uploadFile{
		{field: "a", name: "a.png", contentType: "image/png", content: []byte("a")},
	})
	req.ContentLength = 1 << 20

	if _, err := Ensure(req, Limits{MaxPartSize: 1024, MaxParts: 2, TempDir: t.TempDir()}); err == nil {
		t.Fatal("declared oversize body was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}
}

func TestIngestRawBodyRejectsDeclaredOversize(t *testing.T) {
	req := rawRequest(t, "/upload", "image/png", "tiny")
	req.ContentLength = 1 << 20

	state, err := Ensure(req, Limits{MaxPartSize: 64, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestRawBody(req); err == nil {
		t.Fatal("declared oversize raw body was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}
}

func TestIngestJSONRejectsDeclaredOversize(t *testing.T) {
	req := jsonRequest("application/json", `{"username":"alice"}`)
	req.ContentLength = 1 << 20

	state, err := Ensure(req, Limits{MaxPartSize: 64, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestJSON(req); err == nil {
		t.Fatal("declared oversize JSON body was accepted")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}
}
