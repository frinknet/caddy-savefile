package paths

import (
	"errors"
	"testing"
)

func TestResolveMime(t *testing.T) {
	const spreadsheet = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

	tests := []struct {
		name     string
		mode     string
		declared string
		detected string
		want     string
		wantErr  bool
	}{
		{name: "agreement", mode: VerifyMimeOn, declared: "image/png", detected: "image/png", want: "image/png"},
		{name: "parameters are stripped", mode: VerifyMimeOn, declared: "IMAGE/PNG; charset=x", detected: "image/png", want: "image/png"},
		{name: "active content wins", mode: VerifyMimeOn, declared: "image/png", detected: "text/html", want: "text/html"},
		{name: "svg is active", mode: VerifyMimeOn, declared: "image/png", detected: "image/svg+xml", want: "image/svg+xml"},
		{name: "subtype fidelity is kept", mode: VerifyMimeOn, declared: spreadsheet, detected: "application/zip", want: spreadsheet},
		{name: "no declaration defers to content", mode: VerifyMimeOn, declared: "", detected: "image/png", want: "image/png"},
		{name: "generic declaration defers to content", mode: VerifyMimeOn, declared: "application/octet-stream", detected: "image/png", want: "image/png"},
		{name: "no content keeps the declaration", mode: VerifyMimeOn, declared: "image/png", detected: "", want: "image/png"},
		{name: "nothing at all", mode: VerifyMimeOn, declared: "", detected: "", want: "application/octet-stream"},
		{name: "off trusts the client", mode: VerifyMimeOff, declared: "image/png", detected: "text/html", want: "image/png"},
		{name: "strict agreement", mode: VerifyMimeStrict, declared: "image/png", detected: "image/png", want: "image/png"},
		{name: "strict mismatch", mode: VerifyMimeStrict, declared: "image/png", detected: "text/html", wantErr: true},
		{name: "strict without a declaration", mode: VerifyMimeStrict, declared: "", detected: "image/png", want: "image/png"},
	}

	for _, test := range tests {
		got, err := ResolveMime(test.mode, test.declared, test.detected)
		if test.wantErr {
			if !errors.Is(err, ErrMimeMismatch) {
				t.Errorf("%s: error = %v, want ErrMimeMismatch", test.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", test.name, err)
			continue
		}
		if got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestIsActiveContent(t *testing.T) {
	active := []string{
		"text/html",
		"text/xml",
		"application/xml",
		"application/xhtml+xml",
		"image/svg+xml",
		"application/rss+xml",
		"application/javascript",
		"text/javascript",
	}
	for _, mimeType := range active {
		if !IsActiveContent(mimeType) {
			t.Errorf("IsActiveContent(%q) = false, want true", mimeType)
		}
	}

	for _, mimeType := range []string{"image/png", "application/pdf", "text/plain", "application/zip", "video/mp4", ""} {
		if IsActiveContent(mimeType) {
			t.Errorf("IsActiveContent(%q) = true, want false", mimeType)
		}
	}
}

func TestValidVerifyMime(t *testing.T) {
	for _, mode := range []string{VerifyMimeOn, VerifyMimeOff, VerifyMimeStrict} {
		if !ValidVerifyMime(mode) {
			t.Errorf("ValidVerifyMime(%q) = false", mode)
		}
	}
	for _, mode := range []string{"", "yes", "OFF", "1"} {
		if ValidVerifyMime(mode) {
			t.Errorf("ValidVerifyMime(%q) = true", mode)
		}
	}
}
