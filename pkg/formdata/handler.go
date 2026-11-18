// Package formdata implements the `formdata` directive, which reads a request
// body once and publishes the submitted fields for templates, including a JSON
// body folded into dotted field names. Only POST, PUT and PATCH are handled;
// every other method passes through untouched.
package formdata

import (
	"fmt"
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/formbody"
	"github.com/frinknet/caddy-savefile/pkg/paths"
)

func init() {
	caddy.RegisterModule(FormData{})
}

// FormData parses submitted form fields and publishes them as placeholders,
// without requiring a file upload rule.
type FormData struct {
	// MaxMemory is the largest accepted form field value, in bytes.
	MaxMemory int64 `json:"max_memory,omitempty"`

	logger *zap.Logger

	// MaxParts caps the number of parts in one multipart body.
	MaxParts int `json:"max_parts,omitempty"`

	// VerifyMime controls how a declared MIME type is treated.
	VerifyMime string `json:"verify_mime,omitempty"`

	// TempDir is the scratch directory used while reading the multipart stream.
	TempDir string `json:"temp_dir,omitempty"`
}

// CaddyModule returns the Caddy module information.
func (FormData) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.formdata",
		New: func() caddy.Module { return new(FormData) },
	}
}

// Provision fills unset fields from the global savefile options.
func (fd *FormData) Provision(ctx caddy.Context) error {
	fd.logger = ctx.Logger()
	global := config.Global()
	if fd.MaxMemory <= 0 {
		fd.MaxMemory = global.MaxSize
	}
	if fd.MaxParts <= 0 {
		fd.MaxParts = global.MaxParts
	}
	if fd.TempDir == "" {
		fd.TempDir = global.TempDir
	}
	if fd.VerifyMime == "" {
		fd.VerifyMime = global.VerifyMime
	}
	if err := paths.ProbeWritable(fd.TempDir, 0o700); err != nil {
		return fmt.Errorf("formdata: temp_dir %q is unusable: %w", fd.TempDir, err)
	}
	return nil
}

// log returns the provisioned logger, or a no-op one when Provision has not run.
func (fd FormData) log() *zap.Logger {
	if fd.logger == nil {
		return zap.NewNop()
	}
	return fd.logger
}

// ServeHTTP publishes parsed form fields and passes the request along. The
// parse is shared with savefile when both directives are in the same chain, so
// the body is only read once.
func (fd FormData) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return next.ServeHTTP(w, r)
	}

	state, err := formbody.Ensure(r, formbody.Limits{
		MaxPartSize: fd.MaxMemory,
		MaxParts:    fd.MaxParts,
		TempDir:     fd.TempDir,
		VerifyMime:  fd.VerifyMime,
	})
	if err != nil {
		return err
	}
	defer state.Cleanup()

	if err := state.IngestJSON(r); err != nil {
		return err
	}

	formbody.PublishFields(r, state.Values)

	fd.log().Debug("parsed form body",
		zap.Int("fields", len(state.Values)),
		zap.Int("file_parts", len(state.Parts)),
	)

	return next.ServeHTTP(w, r)
}
