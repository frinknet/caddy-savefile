package paths

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// rootDir is a synthetic root: none of these tests touch the filesystem.
const rootDir = "/root"

// The sanitizer is the security boundary for client-supplied names, so it gets
// fuzz coverage: whatever comes in, nothing may escape the root.
func FuzzSanitizeFilename(f *testing.F) {
	for _, seed := range []string{
		"",
		".",
		"..",
		"../../etc/passwd",
		`..\..\windows\system32\evil.dll`,
		"caf\u00e9.txt",
		"nul\x00name.txt",
		"zero\u200bwidth.txt",
		"a/b/c.txt",
		strings.Repeat("a", 500),
		strings.Repeat("\u00e9", 300),
		"CON",
		"...",
		" leading and trailing ",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		out := SanitizeFilename(name)

		if out == "" {
			t.Fatalf("SanitizeFilename(%q) returned an empty name", name)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("SanitizeFilename(%q) = %q, which is not valid UTF-8", name, out)
		}
		if len(out) > maxFilenameLen {
			t.Fatalf("SanitizeFilename(%q) is %d bytes, over %d", name, len(out), maxFilenameLen)
		}
		if strings.ContainsAny(out, `/\`) {
			t.Fatalf("SanitizeFilename(%q) = %q, which still holds a separator", name, out)
		}
		if out == "." || out == ".." || strings.HasPrefix(out, "..") {
			t.Fatalf("SanitizeFilename(%q) = %q, which is still a traversal", name, out)
		}

		abs, rel, err := ResolveWithinRoot(rootDir, out)
		if err != nil {
			t.Fatalf("ResolveWithinRoot(%q) from %q: %v", out, name, err)
		}
		if rel != out {
			t.Fatalf("ResolveWithinRoot(%q) gave rel %q", out, rel)
		}
		if !strings.HasPrefix(abs, rootDir+"/") {
			t.Fatalf("SanitizeFilename(%q) resolved to %q, outside the root", name, abs)
		}
	})
}

func FuzzSanitizeRelPath(f *testing.F) {
	for _, seed := range []string{
		"",
		"receipts/summary.txt",
		"receipts/../../escape.txt",
		"../../etc/passwd",
		"/absolute/path",
		"./././",
		"a//b//c",
		strings.Repeat("../", 40) + "escape",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		rel, err := SanitizeRelPath(path)
		if err != nil {
			return
		}

		if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
			t.Fatalf("SanitizeRelPath(%q) = %q, which is absolute", path, rel)
		}
		if !utf8.ValidString(rel) {
			t.Fatalf("SanitizeRelPath(%q) = %q, which is not valid UTF-8", path, rel)
		}

		abs, _, err := ResolveWithinRoot(rootDir, rel)
		if err != nil {
			t.Fatalf("SanitizeRelPath(%q) = %q, which escapes the root: %v", path, rel, err)
		}
		if !strings.HasPrefix(abs, rootDir+"/") {
			t.Fatalf("SanitizeRelPath(%q) = %q, resolving to %q", path, rel, abs)
		}

		for _, segment := range strings.Split(rel, "/") {
			if segment == "" || segment == "." || segment == ".." {
				t.Fatalf("SanitizeRelPath(%q) = %q, which keeps %q", path, rel, segment)
			}
		}
	})
}

// Destination templates come from configuration, but {filename} is client data,
// so containment has to hold for arbitrary combinations of the two.
func FuzzResolveDestination(f *testing.F) {
	f.Add("images/{uuid}_{filename}", "photo.png")
	f.Add("{filename}", "notes.txt")
	f.Add("/root/docs/{uuid}", "x.pdf")
	f.Add("a/{uuid}/{filename}", "../../etc/passwd")

	f.Fuzz(func(t *testing.T, template, filename string) {
		abs, rel, err := ResolveDestination(rootDir, template, "00000000-0000-4000-8000-000000000000", SanitizeFilename(filename))
		if err != nil {
			return
		}

		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("ResolveDestination(%q, %q) escaped to %q", template, filename, rel)
		}
		if !strings.HasPrefix(abs, rootDir+string(filepath.Separator)) {
			t.Fatalf("ResolveDestination(%q, %q) = %q, outside the root", template, filename, abs)
		}
		if want := filepath.Join(rootDir, rel); filepath.Clean(abs) != want {
			t.Fatalf("ResolveDestination(%q, %q) = %q, want %q", template, filename, abs, want)
		}
	})
}
