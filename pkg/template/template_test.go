package template

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	caddytpl "github.com/caddyserver/caddy/v2/modules/caddyhttp/templates"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/paths"
)

func newFunc(t *testing.T) (SaveFileFunc, string) {
	t.Helper()
	root := t.TempDir()
	return SaveFileFunc{RootDir: root, MaxSize: config.DefaultMaxSize, TempDir: t.TempDir()}, root
}

func TestSaveWritesString(t *testing.T) {
	function, root := newFunc(t)

	rel, err := function.save("receipts/summary.txt", "notes")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if rel != "receipts/summary.txt" {
		t.Errorf("rel = %q", rel)
	}

	content, err := os.ReadFile(filepath.Join(root, "receipts", "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "notes" {
		t.Errorf("content = %q", content)
	}
}

func TestSaveWritesBinaryUnchanged(t *testing.T) {
	function, root := newFunc(t)
	payload := []byte{0x00, 0x01, 0xff, '\n', 0x0d, 0x0a}

	if _, err := function.save("blob.bin", payload); err != nil {
		t.Fatalf("save: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "blob.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, payload) {
		t.Errorf("content = %x, want %x", content, payload)
	}
}

func TestSaveAcceptsReaderAndStringKinds(t *testing.T) {
	function, root := newFunc(t)

	if _, err := function.save("from-reader.txt", strings.NewReader("reader")); err != nil {
		t.Fatalf("save reader: %v", err)
	}
	if _, err := function.save("from-html.html", htmlString("<b>hi</b>")); err != nil {
		t.Fatalf("save html: %v", err)
	}
	if _, err := function.save("empty.txt", nil); err != nil {
		t.Fatalf("save nil: %v", err)
	}

	for name, want := range map[string]string{
		"from-reader.txt": "reader",
		"from-html.html":  "<b>hi</b>",
		"empty.txt":       "",
	} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != want {
			t.Errorf("%s = %q, want %q", name, content, want)
		}
	}

	if _, err := function.save("bad.txt", 42); err == nil {
		t.Error("non-text content was accepted")
	}
}

func TestSaveSanitizesAndConfines(t *testing.T) {
	function, root := newFunc(t)

	if _, err := function.save("/etc/passwd", "x"); err == nil {
		t.Error("absolute path was accepted")
	}
	if _, err := function.save("..", "x"); err == nil {
		t.Error("traversal-only path was accepted")
	}

	for input, want := range map[string]string{
		"../escape.txt":      "escape.txt",
		"a/../../inside.txt": "a/inside.txt",
		"../../etc/passwd":   "etc/passwd",
	} {
		rel, err := function.save(input, "x")
		if err != nil {
			t.Errorf("save(%q): %v", input, err)
			continue
		}
		if rel != want {
			t.Errorf("save(%q) = %q, want %q", input, rel, want)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
			t.Errorf("save(%q) did not land inside root_dir: %v", input, err)
		}
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Error("write escaped root_dir")
	}
}

func TestSaveEnforcesMaxSize(t *testing.T) {
	root := t.TempDir()
	function := SaveFileFunc{RootDir: root, MaxSize: 4, TempDir: t.TempDir()}

	_, err := function.save("big.txt", "way too long")
	if !errors.Is(err, paths.ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "big.txt")); !os.IsNotExist(statErr) {
		t.Error("partial file left on disk")
	}
}

func TestCustomTemplateFunctions(t *testing.T) {
	function, root := newFunc(t)

	funcs := function.CustomTemplateFunctions()
	if _, ok := funcs["savefile"]; !ok {
		t.Fatal("savefile function is not registered")
	}

	execute := func(source string, data any) (string, error) {
		tpl, err := template.New("test").Funcs(funcs).Parse(source)
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			return "", err
		}
		return buf.String(), nil
	}

	rendered, err := execute(`{{$path := savefile (printf "receipts/%s.txt" "alice") "note"}}{{$path}}`, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if rendered != "receipts/alice.txt" {
		t.Errorf("rendered = %q", rendered)
	}
	if _, err := os.Stat(filepath.Join(root, "receipts", "alice.txt")); err != nil {
		t.Errorf("template write missing: %v", err)
	}

	if _, err := execute(`{{savefile "/etc/passwd" "x"}}`, nil); err == nil {
		t.Error("absolute path was accepted")
	}
}

func TestUnmarshalCaddyfile(t *testing.T) {
	if err := new(SaveFileFunc).UnmarshalCaddyfile(caddyfile.NewTestDispenser("savefile")); err != nil {
		t.Errorf("UnmarshalCaddyfile: %v", err)
	}
	if err := new(SaveFileFunc).UnmarshalCaddyfile(caddyfile.NewTestDispenser("savefile extra")); err == nil {
		t.Error("unexpected argument was accepted")
	}
}

func TestProvisionUsesGlobalConfig(t *testing.T) {
	original := config.Global()
	t.Cleanup(func() { config.Store(original) })

	config.Store(config.Config{RootDir: "/global/root", MaxSize: 512, TempDir: "/global/tmp"})

	function := new(SaveFileFunc)
	if err := function.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	if function.RootDir != "/global/root" || function.MaxSize != 512 || function.TempDir != "/global/tmp" {
		t.Errorf("provisioned = %+v", function)
	}
}

func TestImplementsCustomFunctions(t *testing.T) {
	var _ caddytpl.CustomFunctions = SaveFileFunc{}
}

// htmlString stands in for a defined string type such as template.HTML.
type htmlString string
