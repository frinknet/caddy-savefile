package paths

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "photo.jpg", want: "photo.jpg"},
		{in: "../../etc/passwd", want: "passwd"},
		{in: `..\..\windows\system32\evil.dll`, want: "evil.dll"},
		{in: "/absolute/path/report.pdf", want: "report.pdf"},
		{in: "with\x00null.txt", want: "withnull.txt"},
		{in: "tab\there", want: "tabhere"},
		{in: "..", want: "file"},
		{in: ".", want: "file"},
		{in: "", want: "file"},
		{in: "   ", want: "file"},
		{in: "trailing.", want: "trailing"},
		{in: "bad:name*?.txt", want: "badname.txt"},
		{in: "nul\x00\x01\x02.txt", want: "nul.txt"},
		{in: "zero\u200bwidth.txt", want: "zerowidth.txt"},
	}

	for _, test := range tests {
		if got := SanitizeFilename(test.in); got != test.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestSanitizeFilenameNormalizesUnicode(t *testing.T) {
	precomposed := "caf\u00e9.txt"
	decomposed := "cafe\u0301.txt"

	if got, want := SanitizeFilename(decomposed), SanitizeFilename(precomposed); got != want {
		t.Errorf("SanitizeFilename(%q) = %q, want %q", decomposed, got, want)
	}
}

func TestSanitizeRelPath(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "receipts/summary.txt", want: "receipts/summary.txt"},
		{in: "receipts/../../escape.txt", want: "receipts/escape.txt"},
		{in: "  spaced  /name.txt", want: "spaced/name.txt"},
		{in: "/etc/passwd", wantErr: true},
		{in: "../", wantErr: true},
		{in: "..", wantErr: true},
		{in: "", wantErr: true},
		{in: "././.", wantErr: true},
	}

	for _, test := range tests {
		got, err := SanitizeRelPath(test.in)
		if test.wantErr {
			if err == nil {
				t.Errorf("SanitizeRelPath(%q) = %q, want error", test.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("SanitizeRelPath(%q): %v", test.in, err)
			continue
		}
		if got != test.want {
			t.Errorf("SanitizeRelPath(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestResolveWithinRoot(t *testing.T) {
	root := t.TempDir()

	abs, rel, err := ResolveWithinRoot(root, "a/b.txt")
	if err != nil {
		t.Fatalf("ResolveWithinRoot: %v", err)
	}
	if rel != filepath.Join("a", "b.txt") {
		t.Errorf("rel = %q, want a/b.txt", rel)
	}
	if !strings.HasPrefix(abs, root) {
		t.Errorf("abs = %q, want inside %q", abs, root)
	}

	for _, escape := range []string{"../outside.txt", "a/../../outside.txt", ".."} {
		if _, _, err := ResolveWithinRoot(root, escape); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("ResolveWithinRoot(%q) error = %v, want ErrUnsafePath", escape, err)
		}
	}
}

func TestResolveDestination(t *testing.T) {
	root := t.TempDir()

	abs, rel, err := ResolveDestination(root, "images/{uuid}_{filename}", "abc-123", "photo.jpg")
	if err != nil {
		t.Fatalf("ResolveDestination: %v", err)
	}
	if filepath.Base(abs) != "abc-123_photo.jpg" {
		t.Errorf("base = %q, want abc-123_photo.jpg", filepath.Base(abs))
	}
	if rel != filepath.Join("images", "abc-123_photo.jpg") {
		t.Errorf("rel = %q", rel)
	}

	inside := filepath.Join(root, "docs", "note.pdf")
	if abs, _, err = ResolveDestination(root, inside, "u", "note.pdf"); err != nil {
		t.Fatalf("absolute destination inside root: %v", err)
	}
	if abs != inside {
		t.Errorf("abs = %q, want %q", abs, inside)
	}

	for _, outside := range []string{"/etc/passwd", filepath.Join(root, "..", "escape.txt"), ""} {
		if _, _, err := ResolveDestination(root, outside, "u", "f"); err == nil {
			t.Errorf("ResolveDestination(%q) succeeded, want error", outside)
		}
	}
}

func TestMimeMatches(t *testing.T) {
	tests := []struct {
		filter string
		actual string
		want   bool
	}{
		{filter: "image/*", actual: "image/png", want: true},
		{filter: "image/*", actual: "application/pdf", want: false},
		{filter: "application/pdf", actual: "application/pdf", want: true},
		{filter: "application/pdf", actual: "application/zip", want: false},
		{filter: "*", actual: "video/mp4", want: true},
		{filter: "*/*", actual: "video/mp4", want: true},
		{filter: "", actual: "video/mp4", want: true},
		{filter: "video/*", actual: "", want: false},
		{filter: "IMAGE/*", actual: "Image/PNG", want: true},
	}

	for _, test := range tests {
		if got := MimeMatches(test.filter, test.actual); got != test.want {
			t.Errorf("MimeMatches(%q, %q) = %v, want %v", test.filter, test.actual, got, test.want)
		}
	}
}

func TestNewUUID(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	seen := make(map[string]bool)
	for range 100 {
		id := NewUUID()
		if !pattern.MatchString(id) {
			t.Fatalf("NewUUID() = %q, not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewUUID() repeated %q", id)
		}
		seen[id] = true
	}
}

func TestCopyLimited(t *testing.T) {
	var buf strings.Builder

	n, err := CopyLimited(&buf, strings.NewReader("hello"), 10)
	if err != nil {
		t.Fatalf("CopyLimited: %v", err)
	}
	if n != 5 || buf.String() != "hello" {
		t.Errorf("CopyLimited = %d, %q", n, buf.String())
	}

	buf.Reset()
	if _, err := CopyLimited(&buf, strings.NewReader("hello world"), 4); !errors.Is(err, ErrTooLarge) {
		t.Errorf("CopyLimited error = %v, want ErrTooLarge", err)
	}

	buf.Reset()
	if _, err := CopyLimited(&buf, strings.NewReader("hi"), 0); !errors.Is(err, ErrTooLarge) {
		t.Errorf("CopyLimited with max 0 error = %v, want ErrTooLarge", err)
	}
}

func TestMoveFileAcrossDirs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "nested", "dst.txt")

	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MoveFile(src, dst); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Errorf("dst contents = %q", data)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("src still present after move")
	}
}

func TestSniffMime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, []byte("<!DOCTYPE html><html><body>hi</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := SniffMimePath(path); got != "text/html" {
		t.Errorf("SniffMimePath = %q, want text/html", got)
	}
	if got := SniffMimeReader(strings.NewReader("")); got != "application/octet-stream" {
		t.Errorf("SniffMimeReader(empty) = %q", got)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got := SniffMimeReader(io.LimitReader(file, 512)); got != "text/html" {
		t.Errorf("SniffMimeReader = %q, want text/html", got)
	}
}

func TestNormalizeMime(t *testing.T) {
	tests := map[string]string{
		"":                          "",
		"text/plain; charset=utf-8": "text/plain",
		"  Application/PDF  ":       "application/pdf",
		"MULTIPART/FORM-DATA; x=y":  "multipart/form-data",
	}

	for in, want := range tests {
		if got := NormalizeMime(in); got != want {
			t.Errorf("NormalizeMime(%q) = %q, want %q", in, got, want)
		}
	}
}
