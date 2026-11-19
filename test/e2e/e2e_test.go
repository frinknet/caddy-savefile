// Package e2e exercises the assembled plugin: Caddyfile adaptation, module
// provisioning, the multipart upload path and the templates action.
package e2e

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	_ "github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	_ "github.com/frinknet/caddy-savefile"
)

func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().String()
}

func TestUploadRoundTrip(t *testing.T) {
	listenAddr := freeAddr(t)
	root := t.TempDir()
	site := t.TempDir()
	temp := t.TempDir()

	processPage := filepath.Join(site, "process.html")
	if err := os.CopyFS(site, os.DirFS(fromRepo(t, "www"))); err != nil {
		t.Fatalf("copying www: %v", err)
	}
	if _, err := os.Stat(processPage); err != nil {
		t.Fatalf("process.html missing: %v", err)
	}

	caddyfile := fmt.Sprintf(`{
	admin off
	auto_https off
	persist_config off
	savefile {
		root_dir %s
		max_size 1MB
		temp_dir %s
	}
}

http://%s {
	root * %s
	handle /upload* {
		formdata
		savefile image/* images/{uuid}_{filename}
		savefile application/pdf docs/{uuid}_{filename}
		respond "{{include \"/process.html\"}}" 200
		templates {
			extensions {
				savefile
			}
		}
	}
	file_server
}
`, root, temp, listenAddr, site)

	adapter := caddyconfig.GetAdapter("caddyfile")
	if adapter == nil {
		t.Fatal("caddyfile adapter is not registered")
	}
	config, warnings, err := adapter.Adapt([]byte(caddyfile), nil)
	if err != nil {
		t.Fatalf("adapting Caddyfile: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("adaptation warnings: %+v", warnings)
	}

	if err := caddy.Load(config, true); err != nil {
		t.Fatalf("loading config: %v", err)
	}
	t.Cleanup(func() { caddy.Stop() })

	waitForListener(t, listenAddr)

	client := &http.Client{Timeout: 15 * time.Second}

	body, contentType := uploadBody(t)
	request, err := http.NewRequest(http.MethodPost, "http://"+listenAddr+"/upload", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", contentType)

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("posting upload: %v", err)
	}
	defer response.Body.Close()

	page, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, page)
	}
	rendered := string(page)
	if !strings.Contains(rendered, "Submission received from alice") {
		t.Errorf("template did not read the form field from .Req.Form:\n%s", rendered)
	}
	if !strings.Contains(rendered, "receipts/alice-notes.txt") {
		t.Errorf("template did not report the saved receipt:\n%s", rendered)
	}

	assertSingleFile(t, filepath.Join(root, "images"), uuidPrefixed("photo.png"))
	assertSingleFile(t, filepath.Join(root, "docs"), uuidPrefixed("doc.pdf"))

	receipt, err := os.ReadFile(filepath.Join(root, "receipts", "alice-notes.txt"))
	if err != nil {
		t.Fatalf("reading receipt: %v", err)
	}
	if string(receipt) != "hello from the form" {
		t.Errorf("receipt = %q", receipt)
	}

	if entries, err := os.ReadDir(temp); err == nil && len(entries) != 0 {
		t.Errorf("temp_dir was not cleaned up: %v", entries)
	}

	postRawBody(t, client, listenAddr)
	assertRawBodyUpload(t, filepath.Join(root, "images"))
}

// postRawBody sends a single file as a plain request body, with no multipart
// envelope, and expects it to be routed by MIME type.
func postRawBody(t *testing.T, client *http.Client, listenAddr string) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, "http://"+listenAddr+"/upload",
		strings.NewReader("\x89PNG raw body, not multipart"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "image/png")

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("posting raw body: %v", err)
	}
	defer response.Body.Close()

	page, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("raw body status = %d, body = %s", response.StatusCode, page)
	}
}

func assertRawBodyUpload(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != 2 {
		t.Fatalf("%s holds %d files, want 2 (multipart plus raw body)", dir, len(entries))
	}

	for _, entry := range entries {
		if uuidPrefixed("upload.png").MatchString(entry.Name()) {
			return
		}
	}
	t.Errorf("%s holds no raw-body upload named <uuid>_upload.png: %v", dir, entries)
}

func assertSingleFile(t *testing.T, dir string, name *regexp.Regexp) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("%s holds %d files, want 1", dir, len(entries))
	}
	if !name.MatchString(entries[0].Name()) {
		t.Errorf("%s: %q does not match %v", dir, entries[0].Name(), name)
	}
}

const uuidV4 = `[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`

func uuidPrefixed(name string) *regexp.Regexp {
	return regexp.MustCompile(`^` + uuidV4 + regexp.QuoteMeta("_"+name) + `$`)
}

func uploadBody(t *testing.T) (io.Reader, string) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for name, value := range map[string]string{
		"username": "alice",
		"notes":    "hello from the form",
	} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}

	files := []struct {
		field       string
		name        string
		contentType string
		content     string
	}{
		{field: "photo", name: "photo.png", contentType: "image/png", content: "\x89PNG fake bytes"},
		{field: "doc", name: "doc.pdf", contentType: "application/pdf", content: "%PDF-1.4 fake"},
		{field: "clip", name: "clip.mp4", contentType: "video/mp4", content: "ignored"},
	}
	for _, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, file.field, file.name))
		header.Set("Content-Type", file.contentType)

		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(file.content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener %s never came up", addr)
}

func fromRepo(t *testing.T, name string) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate %s from the repo root", name)
	return ""
}
