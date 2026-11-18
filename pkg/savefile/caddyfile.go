package savefile

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/config"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("savefile", parseCaddyfile)
	httpcaddyfile.RegisterDirectiveOrder("savefile", httpcaddyfile.Before, "templates")
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	handler := new(SaveFile)
	if global, ok := h.Option("savefile").(*config.Config); ok && global != nil {
		handler.RootDir = global.RootDir
		handler.TempDir = global.TempDir
		handler.MaxSize = global.MaxSize
	}

	if err := handler.UnmarshalCaddyfile(h.Dispenser); err != nil {
		return nil, err
	}
	return handler, nil
}

// UnmarshalCaddyfile parses the `savefile <mimetype_filter> <destination>`
// directive.
func (sf *SaveFile) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if !d.NextArg() {
			return d.ArgErr()
		}
		sf.MimeFilter = d.Val()

		if !d.NextArg() {
			return d.ArgErr()
		}
		sf.Destination = d.Val()

		if d.NextArg() {
			return d.ArgErr()
		}
	}
	return nil
}
