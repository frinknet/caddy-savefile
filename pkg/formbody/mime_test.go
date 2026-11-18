package formbody

import (
	"net/http"
	"testing"

	"github.com/frinknet/caddy-savefile/pkg/paths"
)

const (
	pngBytes  = "\x89PNG\r\n\x1a\n fake png payload"
	htmlBytes = "<!DOCTYPE html><html><body>not an image</body></html>"
	zipBytes  = "\x50\x4b\x03\x04 fake container"
)

func TestPartRecordsDeclaredAndDetectedTypes(t *testing.T) {
	req := multipartRequest(t, nil, []uploadFile{
		{field: "photo", name: "photo.png", contentType: "image/png", content: []byte(pngBytes)},
		{field: "page", name: "page.png", contentType: "image/png", content: []byte(htmlBytes)},
	})

	state, err := Ensure(req, Limits{MaxPartSize: 4096, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer state.Cleanup()

	if len(state.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(state.Parts))
	}

	photo := state.Parts[0]
	if photo.DeclaredMimeType != "image/png" || photo.DetectedMimeType != "image/png" {
		t.Errorf("photo types = %q/%q", photo.DeclaredMimeType, photo.DetectedMimeType)
	}
	if photo.MimeType != "image/png" {
		t.Errorf("photo routed as %q, want image/png", photo.MimeType)
	}

	page := state.Parts[1]
	if page.DeclaredMimeType != "image/png" {
		t.Errorf("page declared = %q, want image/png", page.DeclaredMimeType)
	}
	if page.DetectedMimeType != "text/html" {
		t.Errorf("page detected = %q, want text/html", page.DetectedMimeType)
	}
	if page.MimeType != "text/html" {
		t.Errorf("page routed as %q; active content must not pass as an image", page.MimeType)
	}
}

func TestPartKeepsFineGrainedDeclarations(t *testing.T) {
	const spreadsheet = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

	req := multipartRequest(t, nil, []uploadFile{
		{field: "sheet", name: "sheet.xlsx", contentType: spreadsheet, content: []byte(zipBytes)},
	})

	state, err := Ensure(req, Limits{MaxPartSize: 4096, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer state.Cleanup()

	part := state.Parts[0]
	if part.DetectedMimeType != "application/zip" {
		t.Errorf("detected = %q, want application/zip", part.DetectedMimeType)
	}
	if part.MimeType != spreadsheet {
		t.Errorf("routed as %q; sniffing must not degrade a fine-grained declaration", part.MimeType)
	}
}

func TestRawBodyIsClassified(t *testing.T) {
	req := rawRequest(t, "/upload", "image/png", htmlBytes)

	state, err := Ensure(req, Limits{MaxPartSize: 4096, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer state.Cleanup()

	if err := state.IngestRawBody(req); err != nil {
		t.Fatalf("IngestRawBody: %v", err)
	}
	if len(state.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(state.Parts))
	}

	part := state.Parts[0]
	if part.MimeType != "text/html" || part.DeclaredMimeType != "image/png" || part.DetectedMimeType != "text/html" {
		t.Errorf("classification = %q/%q/%q", part.MimeType, part.DeclaredMimeType, part.DetectedMimeType)
	}
	if part.Filename != "upload.html" {
		t.Errorf("filename = %q, want the extension of the routed type", part.Filename)
	}
}

func TestVerifyMimeStrictRejectsMismatch(t *testing.T) {
	req := multipartRequest(t, nil, []uploadFile{
		{field: "page", name: "page.png", contentType: "image/png", content: []byte(htmlBytes)},
	})

	limits := Limits{MaxPartSize: 4096, TempDir: t.TempDir(), VerifyMime: paths.VerifyMimeStrict}
	if _, err := Ensure(req, limits); err == nil {
		t.Fatal("strict verification accepted a mismatch")
	} else if status, ok := errorStatus(err); !ok || status != http.StatusUnsupportedMediaType {
		t.Errorf("error = %v, want 415", err)
	}
}

func TestVerifyMimeOffTrustsTheClient(t *testing.T) {
	req := multipartRequest(t, nil, []uploadFile{
		{field: "page", name: "page.png", contentType: "image/png", content: []byte(htmlBytes)},
	})

	limits := Limits{MaxPartSize: 4096, TempDir: t.TempDir(), VerifyMime: paths.VerifyMimeOff}
	state, err := Ensure(req, limits)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer state.Cleanup()

	if got := state.Parts[0].MimeType; got != "image/png" {
		t.Errorf("routed as %q, want the declared type when verification is off", got)
	}
}
