package formdata

import (
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/formbody"
)

func init() {
	caddy.RegisterModule(FormData{})
}

// FormData parses submitted form fields and publishes them as placeholders,
// without requiring a file upload rule.
type FormData struct {
	// MaxMemory is the largest accepted form field value, in bytes.
	MaxMemory int64 `json:"max_memory,omitempty"`

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
func (fd *FormData) Provision(caddy.Context) error {
	global := config.Global()
	if fd.MaxMemory <= 0 {
		fd.MaxMemory = global.MaxSize
	}
	if fd.TempDir == "" {
		fd.TempDir = global.TempDir
	}
	return nil
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

	state, err := formbody.Ensure(r, fd.MaxMemory, fd.TempDir)
	if err != nil {
		return err
	}

	if err := state.IngestJSON(r); err != nil {
		return err
	}

	formbody.PublishFields(r, state.Values)
	return next.ServeHTTP(w, r)
}
