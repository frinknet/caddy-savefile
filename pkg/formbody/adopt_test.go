package formbody

import (
	"io"
	"testing"
)

// A module earlier in the chain may have parsed the form with
// r.ParseMultipartForm, in which case the parts are already in memory and must
// be adopted rather than read again.
func TestEnsureAdoptsExternallyParsedForm(t *testing.T) {
	req := multipartRequest(t, map[string]string{"username": "alice"}, []uploadFile{
		{field: "photo", name: "photo.png", contentType: "image/png", content: []byte(pngBytes)},
	})
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}

	state, err := Ensure(req, Limits{MaxPartSize: 4096, MaxParts: 10, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer state.Cleanup()

	if got := state.Values.Get("username"); got != "alice" {
		t.Errorf("username = %q, want the adopted value", got)
	}
	if len(state.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(state.Parts))
	}

	part := state.Parts[0]
	if part.Filename != "photo.png" || part.MimeType != "image/png" {
		t.Errorf("adopted part = %+v", part)
	}
	if part.Header == nil {
		t.Error("adopted part lost its file header")
	}
	if part.TempPath != "" {
		t.Error("adopted part was buffered to a temp file unnecessarily")
	}

	reader, err := part.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pngBytes {
		t.Errorf("adopted content = %q", content)
	}
}

func TestEnsureAdoptEnforcesPartCap(t *testing.T) {
	req := multipartRequest(t, nil, []uploadFile{
		{field: "a", name: "a.png", contentType: "image/png", content: []byte(pngBytes)},
		{field: "b", name: "b.png", contentType: "image/png", content: []byte(pngBytes)},
	})
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}

	limits := Limits{MaxPartSize: 4096, MaxParts: 1, TempDir: t.TempDir()}
	if _, err := Ensure(req, limits); err == nil {
		t.Error("the part cap was not applied to an already parsed form")
	}
}
