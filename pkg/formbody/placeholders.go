package formbody

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"

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

// PublishSavedFile exposes the file that was just written as
// {http.vars.savefile.*} placeholders. The vars table is flat, so a request
// that writes several files leaves the metadata of the last one in place.
// relativePath is the destination relative to root_dir, not the path on disk.
func PublishSavedFile(r *http.Request, info SavedFileInfo, relativePath string) {
	repl := Replacer(r)
	if repl == nil {
		return
	}
	repl.Set("http.vars.savefile.filename", info.OriginalFilename)
	repl.Set("http.vars.savefile.saved_path", filepath.ToSlash(relativePath))
	repl.Set("http.vars.savefile.filesize", strconv.FormatInt(info.Filesize, 10))
	repl.Set("http.vars.savefile.mimetype", info.MimeType)
}
