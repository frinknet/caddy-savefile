// Package formbody consumes a request body exactly once and publishes the
// resulting form fields and multipart file parts for the formdata and savefile
// directives.
package formbody

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/paths"
)

type ctxKey struct{}

// Part is a buffered multipart file part. A part is claimed by at most one
// savefile rule per request.
type Part struct {
	FieldName string
	Filename  string
	MimeType  string
	Size      int64

	TempPath string
	Header   *multipart.FileHeader

	Saved bool
}

// SavedFileInfo mirrors the metadata published for every file written to disk.
type SavedFileInfo struct {
	FieldName        string `json:"field_name"`
	OriginalFilename string `json:"original_filename"`
	SavedPath        string `json:"saved_path"`
	Filesize         int64  `json:"filesize"`
	MimeType         string `json:"mime_type"`
}

// FormState is the per-request parse result shared by every handler that takes
// part in form processing.
type FormState struct {
	Values url.Values
	Parts  []*Part
	Files  []SavedFileInfo

	maxSize   int64
	tempDir   string
	tempPaths []string
	consumed  bool
	cleanOnce sync.Once
}

// Consumed reports whether the request body has already been read, by a form
// parse or by a raw-body ingest.
func (st *FormState) Consumed() bool {
	return st.consumed
}

// FromRequest returns the form state already attached to r, if any.
func FromRequest(r *http.Request) *FormState {
	state, _ := r.Context().Value(ctxKey{}).(*FormState)
	return state
}

// Ensure parses r's body once and attaches the result to the request. Multipart
// bodies are read through their reader into temp files so later handlers can
// still route the file parts.
func Ensure(r *http.Request, maxSize int64, tempDir string) (*FormState, error) {
	if state := FromRequest(r); state != nil {
		return state, nil
	}

	if maxSize <= 0 {
		maxSize = config.Global().MaxSize
	}
	if tempDir == "" {
		tempDir = config.Global().TempDir
	}

	state := &FormState{Values: url.Values{}, maxSize: maxSize, tempDir: tempDir}

	var err error
	switch paths.NormalizeMime(r.Header.Get("Content-Type")) {
	case "multipart/form-data":
		state.consumed = true
		form := r.MultipartForm
		switch {
		case form != nil && (len(form.Value) > 0 || len(form.File) > 0):
			state.adopt(form)
		case form != nil:
			// An earlier handler already consumed the multipart stream, leaving
			// an empty form behind. There is nothing left to read.
		default:
			err = state.consumeMultipart(r)
		}
	case "application/x-www-form-urlencoded":
		state.consumed = true
		if parseErr := r.ParseForm(); parseErr != nil {
			err = caddyhttp.Error(http.StatusBadRequest, parseErr)
		} else {
			for key, vals := range r.PostForm {
				state.Values[key] = vals
			}
		}
	default:
		// JSON and raw bodies are claimed on demand: formdata folds JSON into
		// fields, savefile buffers anything else as a single file part.
	}
	if err != nil {
		state.Cleanup()
		return nil, err
	}

	state.attachForm(r)

	ctx := context.WithValue(r.Context(), ctxKey{}, state)
	*r = *r.WithContext(ctx)
	context.AfterFunc(ctx, state.Cleanup)

	return state, nil
}

// attachForm puts the parsed body values on the request the way the standard
// library does, so templates reach them through .Req.Form without any
// plugin-specific surface. Body values come first, matching ParseForm, and the
// URL query is merged after them.
func (st *FormState) attachForm(r *http.Request) {
	post := url.Values{}
	for key, vals := range st.Values {
		post[key] = append([]string(nil), vals...)
	}
	r.PostForm = post

	form := url.Values{}
	for key, vals := range post {
		form[key] = append([]string(nil), vals...)
	}
	if r.URL != nil {
		if query, err := url.ParseQuery(r.URL.RawQuery); err == nil {
			for key, vals := range query {
				form[key] = append(form[key], vals...)
			}
		}
	}
	r.Form = form
}

// Cleanup removes every temp file that was not moved to its destination.
func (st *FormState) Cleanup() {
	st.cleanOnce.Do(func() {
		for _, path := range st.tempPaths {
			os.Remove(path)
		}
	})
}

// Open opens the part's source for reading.
func (p *Part) Open() (io.ReadCloser, error) {
	if p.TempPath != "" {
		return os.Open(p.TempPath)
	}
	if p.Header != nil {
		return p.Header.Open()
	}
	return nil, errors.New("form part has no source")
}

// adopt imports file parts that another module already parsed.
func (st *FormState) adopt(form *multipart.Form) {
	for key, vals := range form.Value {
		for _, val := range vals {
			st.Values.Add(key, val)
		}
	}

	fields := make([]string, 0, len(form.File))
	for field := range form.File {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	for _, field := range fields {
		for _, header := range form.File[field] {
			st.Parts = append(st.Parts, &Part{
				FieldName: field,
				Filename:  header.Filename,
				MimeType:  st.mimeTypeOf(header),
				Size:      header.Size,
				Header:    header,
			})
		}
	}
}

func (st *FormState) mimeTypeOf(header *multipart.FileHeader) string {
	if mimeType := paths.NormalizeMime(header.Header.Get("Content-Type")); mimeType != "" {
		return mimeType
	}

	file, err := header.Open()
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	return paths.SniffMimeReader(file)
}
