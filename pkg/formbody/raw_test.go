package formbody

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func rawRequest(t *testing.T, target, contentType, body string) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return withReplacer(req)
}

func TestIngestRawBodyBuffersTheBody(t *testing.T) {
	tempDir := t.TempDir()
	req := rawRequest(t, "/upload/photo.png", "image/png", "png-bytes")

	state, err := Ensure(req, 4096, tempDir)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestRawBody(req); err != nil {
		t.Fatalf("IngestRawBody: %v", err)
	}

	if len(state.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(state.Parts))
	}
	part := state.Parts[0]
	if part.Filename != "photo.png" {
		t.Errorf("filename = %q, want the last path segment", part.Filename)
	}
	if part.MimeType != "image/png" {
		t.Errorf("mime = %q", part.MimeType)
	}
	if part.Size != int64(len("png-bytes")) {
		t.Errorf("size = %d", part.Size)
	}
	if !state.Consumed() {
		t.Error("raw body did not mark the state consumed")
	}

	content, err := os.ReadFile(part.TempPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "png-bytes" {
		t.Errorf("buffered content = %q", content)
	}
}

func TestIngestRawBodyDerivesFilenameAndSniffsMime(t *testing.T) {
	req := rawRequest(t, "/upload", "application/octet-stream", "<!DOCTYPE html><html></html>")

	state, err := Ensure(req, 4096, t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestRawBody(req); err != nil {
		t.Fatalf("IngestRawBody: %v", err)
	}

	part := state.Parts[0]
	if part.MimeType != "text/html" {
		t.Errorf("mime = %q, want the sniffed type", part.MimeType)
	}
	if part.Filename != "upload.html" {
		t.Errorf("filename = %q, want upload.html", part.Filename)
	}
}

func TestIngestRawBodyHonoursContentDisposition(t *testing.T) {
	req := rawRequest(t, "/upload", "application/pdf", "%PDF-1.4")
	req.Header.Set("Content-Disposition", `attachment; filename="report.pdf"`)

	state, err := Ensure(req, 4096, t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestRawBody(req); err != nil {
		t.Fatalf("IngestRawBody: %v", err)
	}

	if got := state.Parts[0].Filename; got != "report.pdf" {
		t.Errorf("filename = %q", got)
	}
}

func TestIngestRawBodyLeavesClaimedBodiesAlone(t *testing.T) {
	req := formRequest(url.Values{"username": {"alice"}})

	state, err := Ensure(req, 4096, t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestRawBody(req); err != nil {
		t.Fatalf("IngestRawBody: %v", err)
	}

	if len(state.Parts) != 0 {
		t.Errorf("parts = %d, want none for a urlencoded body", len(state.Parts))
	}
}

func TestIngestRawBodyIgnoresEmptyBodies(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/upload", nil)
	req.Header.Set("Content-Type", "image/png")

	state, err := Ensure(req, 4096, t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestRawBody(req); err != nil {
		t.Fatalf("IngestRawBody: %v", err)
	}

	if len(state.Parts) != 0 {
		t.Errorf("parts = %d, want none for an empty body", len(state.Parts))
	}
}

func TestIngestRawBodyRejectsOversizeBody(t *testing.T) {
	req := rawRequest(t, "/upload", "application/octet-stream", strings.Repeat("x", 8192))

	state, err := Ensure(req, 16, t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	err = state.IngestRawBody(req)
	if err == nil {
		t.Fatal("oversize body was accepted")
	}
	if status, ok := errorStatus(err); !ok || status != http.StatusRequestEntityTooLarge {
		t.Errorf("error = %v, want 413", err)
	}

	for _, path := range state.tempPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("temp file %q survived cleanup", path)
		}
	}
}
