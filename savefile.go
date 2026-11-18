// Package caddysavefile is the entry point for the Caddy form-processing and
// file-upload suite. Importing it registers the http.handlers.savefile and
// http.handlers.formdata directives, the `savefile` global options block, and
// the `savefile` action for the templates handler.
package caddysavefile

import (
	_ "github.com/frinknet/caddy-savefile/pkg/config"
	_ "github.com/frinknet/caddy-savefile/pkg/formdata"
	_ "github.com/frinknet/caddy-savefile/pkg/savefile"
	_ "github.com/frinknet/caddy-savefile/pkg/template"
)
