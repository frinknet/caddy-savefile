package formdata

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/config"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("formdata", parseCaddyfile)
	httpcaddyfile.RegisterDirectiveOrder("formdata", httpcaddyfile.After, "request_body")
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	handler := new(FormData)
	if global, ok := h.Option("savefile").(*config.Config); ok && global != nil {
		handler.MaxMemory = global.MaxSize
		handler.TempDir = global.TempDir
	}

	if err := handler.UnmarshalCaddyfile(h.Dispenser); err != nil {
		return nil, err
	}
	return handler, nil
}

// UnmarshalCaddyfile parses the bare `formdata` directive.
func (fd *FormData) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
	}
	return nil
}
