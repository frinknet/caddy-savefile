package formbody

import (
	"net/http"
	"net/url"

	"github.com/caddyserver/caddy/v2"
)

// Replacer returns the request's placeholder replacer, if one is installed.
func Replacer(r *http.Request) *caddy.Replacer {
	repl, _ := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	return repl
}

// PublishFields exposes parsed form fields as {http.vars.form.<key>}. The vars
// table is flat, so every key becomes its own placeholder rather than a branch
// of one tree.
func PublishFields(r *http.Request, values url.Values) {
	repl := Replacer(r)
	if repl == nil {
		return
	}
	for key, vals := range values {
		if len(vals) == 0 {
			continue
		}
		repl.Set("http.vars.form."+key, vals[0])
	}
}
