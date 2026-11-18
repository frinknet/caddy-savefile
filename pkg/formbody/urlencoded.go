package formbody

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/paths"
)

// ingestURLEncoded reads an application/x-www-form-urlencoded body within the
// configured limit. http.Request.ParseForm cannot be used here because it reads
// an unbounded body into memory.
func (st *FormState) ingestURLEncoded(r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	if r.ContentLength > st.limits.MaxPartSize {
		return caddyhttp.Error(http.StatusRequestEntityTooLarge, paths.ErrTooLarge)
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, st.limits.MaxPartSize+1))
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("reading form body: %w", err))
	}
	if int64(len(raw)) > st.limits.MaxPartSize {
		return caddyhttp.Error(http.StatusRequestEntityTooLarge, paths.ErrTooLarge)
	}

	values, err := url.ParseQuery(string(raw))
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("parsing form body: %w", err))
	}

	for key, vals := range values {
		st.Values[key] = vals
	}
	return nil
}
