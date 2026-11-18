// Package template adds the `savefile` action to Caddy's templates handler.
package template

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	caddytpl "github.com/caddyserver/caddy/v2/modules/caddyhttp/templates"
	"go.uber.org/zap"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/paths"
)

func init() {
	caddy.RegisterModule(SaveFileFunc{})
}

// SaveFileFunc implements {{ savefile <filename> <content> }}, writing text or
// binary content into root_dir from within a template.
type SaveFileFunc struct {
	// RootDir is the base directory every write is confined to.
	RootDir string `json:"root_dir,omitempty"`

	// MaxSize is the largest accepted payload, in bytes.
	MaxSize int64 `json:"max_size,omitempty"`

	// TempDir is the scratch directory used while writing.
	TempDir string `json:"temp_dir,omitempty"`

	logger *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (SaveFileFunc) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.templates.functions.savefile",
		New: func() caddy.Module { return new(SaveFileFunc) },
	}
}

// Provision fills unset fields from the global savefile options.
func (f *SaveFileFunc) Provision(ctx caddy.Context) error {
	f.logger = ctx.Logger()
	global := config.Global()
	if f.RootDir == "" {
		f.RootDir = global.RootDir
	}
	if f.MaxSize <= 0 {
		f.MaxSize = global.MaxSize
	}
	if f.TempDir == "" {
		f.TempDir = global.TempDir
	}
	if err := paths.ProbeWritable(f.RootDir, 0o755); err != nil {
		return fmt.Errorf("savefile: root_dir %q is unusable: %w", f.RootDir, err)
	}
	if err := paths.ProbeWritable(f.TempDir, 0o700); err != nil {
		return fmt.Errorf("savefile: temp_dir %q is unusable: %w", f.TempDir, err)
	}
	return nil
}

// UnmarshalCaddyfile accepts the bare extension name.
func (f *SaveFileFunc) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
	}
	return nil
}

// CustomTemplateFunctions exposes the savefile action to templates.
func (f SaveFileFunc) CustomTemplateFunctions() template.FuncMap {
	return template.FuncMap{"savefile": f.save}
}

func (f SaveFileFunc) save(filename string, content any) (string, error) {
	clean, err := paths.SanitizeRelPath(filename)
	if err != nil {
		return "", fmt.Errorf("savefile: %w", err)
	}

	destination, relative, err := paths.ResolveWithinRoot(f.RootDir, clean)
	if err != nil {
		return "", fmt.Errorf("savefile: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", fmt.Errorf("savefile: %w", err)
	}

	source, err := readerFor(content)
	if err != nil {
		return "", err
	}
	if closer, ok := source.(io.Closer); ok {
		defer closer.Close()
	}

	if err := paths.EnsureDir(f.tempDir(), 0o700); err != nil {
		return "", fmt.Errorf("savefile: %w", err)
	}

	temp, err := os.CreateTemp(f.tempDir(), "caddy-savefile-*")
	if err != nil {
		return "", fmt.Errorf("savefile: %w", err)
	}
	tempPath := temp.Name()

	size, err := paths.CopyLimited(temp, source, f.maxSize())
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("savefile: %w", err)
	}

	if err := paths.MoveFile(tempPath, destination); err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("savefile: %w", err)
	}

	f.log().Debug("wrote template output",
		zap.String("path", relative),
		zap.Int64("bytes", size),
	)

	return filepath.ToSlash(relative), nil
}

// log returns the provisioned logger, or a no-op one when Provision has not run.
func (f SaveFileFunc) log() *zap.Logger {
	if f.logger == nil {
		return zap.NewNop()
	}
	return f.logger
}

func (f SaveFileFunc) maxSize() int64 {
	if f.MaxSize > 0 {
		return f.MaxSize
	}
	return config.Global().MaxSize
}

func (f SaveFileFunc) tempDir() string {
	if f.TempDir != "" {
		return f.TempDir
	}
	return config.Global().TempDir
}

func readerFor(content any) (io.Reader, error) {
	switch value := content.(type) {
	case nil:
		return strings.NewReader(""), nil
	case string:
		return strings.NewReader(value), nil
	case []byte:
		return bytes.NewReader(value), nil
	case io.Reader:
		return value, nil
	}

	if reflected := reflect.ValueOf(content); reflected.IsValid() && reflected.Kind() == reflect.String {
		return strings.NewReader(reflected.String()), nil
	}
	if stringer, ok := content.(fmt.Stringer); ok {
		return strings.NewReader(stringer.String()), nil
	}

	return nil, fmt.Errorf("savefile: unsupported content type %T", content)
}

var _ caddytpl.CustomFunctions = SaveFileFunc{}
