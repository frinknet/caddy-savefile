// Package paths owns the filesystem security invariants shared by the savefile
// directives: unconditional filename sanitization, root_dir containment,
// payload size enforcement, and the small I/O helpers built on top of them.
package paths

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Errors reported by this package. Callers map ErrTooLarge to 413 and
// ErrUnsafePath to 400.
var (
	ErrTooLarge    = errors.New("payload exceeds max_size")
	ErrUnsafePath  = errors.New("path escapes root_dir")
	ErrEmptyPath   = errors.New("path has no usable segments")
	errNoPartSink  = errors.New("no destination available")
	maxFilenameLen = 200
)

// SanitizeFilename strips every directory component, unsafe character and
// traversal segment from a client-supplied filename. The result is always a
// single path segment and is never empty.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}

	name = norm.NFC.String(name)

	var b strings.Builder
	for _, r := range name {
		switch {
		case r == 0 || r < 0x20 || r == 0x7f:
			continue
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|':
			continue
		case unicode.In(r, unicode.Cf):
			continue
		}
		b.WriteRune(r)
	}

	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, " .")
	if out == "" {
		return "file"
	}
	if len(out) > maxFilenameLen {
		out = out[:maxFilenameLen]
	}
	return out
}

// SanitizeRelPath sanitizes each segment of a relative subpath, dropping empty
// segments and traversal elements. It returns an error if nothing usable
// remains.
func SanitizeRelPath(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: %q must be relative", ErrUnsafePath, p)
	}

	segments := strings.Split(p, "/")
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		sanitized := SanitizeFilename(seg)
		if sanitized == "" || sanitized == "file" && seg != "file" {
			continue
		}
		out = append(out, sanitized)
	}
	if len(out) == 0 {
		return "", ErrEmptyPath
	}
	return strings.Join(out, "/"), nil
}

// ResolveWithinRoot joins rel onto root and verifies the result stays inside
// root. The returned abs path is absolute; relOut is relative to root.
func ResolveWithinRoot(root, rel string) (abs, relOut string, err error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}

	target := filepath.Clean(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	within, err := relativeWithin(rootAbs, target)
	if err != nil {
		return "", "", err
	}
	return target, within, nil
}

// ResolveDestination expands the {uuid} and {filename} tokens in a directive
// destination template and confines the result to root. Absolute destinations
// are allowed as long as they land inside root.
func ResolveDestination(root, tmpl, uuid, filename string) (abs, rel string, err error) {
	expanded := strings.NewReplacer(
		"{uuid}", uuid,
		"{filename}", filename,
	).Replace(tmpl)
	expanded = strings.TrimSpace(expanded)
	if expanded == "" {
		return "", "", errors.New("empty savefile destination")
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}

	target := filepath.Clean(filepath.FromSlash(expanded))
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(rootAbs, target))
	}

	within, err := relativeWithin(rootAbs, target)
	if err != nil {
		return "", "", err
	}
	return target, within, nil
}

func relativeWithin(rootAbs, target string) (string, error) {
	within, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	if within == "." || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, target)
	}
	return within, nil
}

// MimeMatches reports whether an actual MIME type satisfies a filter written as
// an exact type, a wildcard subtype such as image/*, or the catch-all *.
func MimeMatches(filter, actual string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	actual = strings.ToLower(strings.TrimSpace(actual))

	if filter == "" || filter == "*" || filter == "*/*" {
		return true
	}
	if actual == "" {
		return false
	}
	if actual == filter {
		return true
	}

	filterType, filterSub := splitMime(filter)
	actualType, actualSub := splitMime(actual)
	if filterType != "*" && filterType != actualType {
		return false
	}
	return filterSub == "*" || filterSub == actualSub
}

func splitMime(s string) (string, string) {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// EnsureDir creates dir and its parents when dir is set.
func EnsureDir(dir string) error {
	if dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

// NewUUID returns a random RFC 4122 version 4 UUID.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("savefile: reading random bytes: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CopyLimited copies src into dst, failing with ErrTooLarge once more than
// maxSize bytes have been read.
func CopyLimited(dst io.Writer, src io.Reader, maxSize int64) (int64, error) {
	if maxSize <= 0 {
		return 0, ErrTooLarge
	}
	n, err := io.Copy(dst, io.LimitReader(src, maxSize+1))
	if err != nil {
		return n, err
	}
	if n > maxSize {
		return n, ErrTooLarge
	}
	return n, nil
}

// CopyFile writes src to path, removing a partial file when the size limit is
// exceeded.
func CopyFile(path string, src io.Reader, maxSize int64) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := CopyLimited(out, src, maxSize)
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(path)
		return copyErr
	}
	return nil
}

// MoveFile renames src to dst, falling back to a copy when the two paths live
// on different filesystems.
func MoveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// SniffMimePath sniffs the MIME type of a file from its leading bytes.
func SniffMimePath(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	return SniffMimeReader(f)
}

// SniffMimeReader sniffs the MIME type from the leading bytes of r.
func SniffMimeReader(r io.Reader) string {
	buf := make([]byte, 512)
	n, err := io.ReadFull(r, buf)
	if n == 0 {
		if err == nil {
			err = io.EOF
		}
		_ = err
		return "application/octet-stream"
	}
	detected := http.DetectContentType(buf[:n])
	if parsed, _, err := mime.ParseMediaType(detected); err == nil {
		return parsed
	}
	return detected
}

// NormalizeMime lowercases a MIME type and strips any parameters.
func NormalizeMime(value string) string {
	if value == "" {
		return ""
	}
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		value = parsed
	}
	return strings.ToLower(strings.TrimSpace(value))
}
