package formbody

import (
	"context"
	"os"
	"testing"
)

// A client that closes the connection mid-upload cancels the request context.
// That must not remove temp files the handlers still need, which is why cleanup
// is deferred by the handlers rather than attached to the context.
func TestTempFilesSurviveClientDisconnect(t *testing.T) {
	tempDir := t.TempDir()
	req := rawRequest(t, "/upload", "image/png", "png-bytes")

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	cancel()

	state, err := Ensure(req, Limits{MaxPartSize: 1024, TempDir: tempDir})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := state.IngestRawBody(req); err != nil {
		t.Fatalf("IngestRawBody: %v", err)
	}

	if len(state.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(state.Parts))
	}
	if _, err := os.Stat(state.Parts[0].TempPath); err != nil {
		t.Errorf("temp file was removed while the request was still running: %v", err)
	}

	state.Cleanup()
	if _, err := os.Stat(state.Parts[0].TempPath); !os.IsNotExist(err) {
		t.Errorf("temp file survived an explicit cleanup: %v", err)
	}
}

func TestMultipartTempFilesSurviveClientDisconnect(t *testing.T) {
	tempDir := t.TempDir()
	req := multipartRequest(t, nil, []uploadFile{
		{field: "photo", name: "photo.png", contentType: "image/png", content: []byte("png-bytes")},
	})

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	cancel()

	state, err := Ensure(req, Limits{MaxPartSize: 1024, TempDir: tempDir})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer state.Cleanup()

	if len(state.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(state.Parts))
	}
	if _, err := os.Stat(state.Parts[0].TempPath); err != nil {
		t.Errorf("temp file was removed while the request was still running: %v", err)
	}
}
