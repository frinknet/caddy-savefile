// Package formbody consumes a request body exactly once and publishes the
// resulting form fields and multipart file parts for the formdata and savefile
// directives.
package formbody

import (
	"context"
	"errors"
	"fmt"
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

// Limits caps how much of a request body is read and kept. Every part is capped
// in size and the number of parts is capped, so total disk and memory use is
// bounded by construction rather than by trusting the client.
type Limits struct {
	MaxPartSize int64
	MaxParts    int
	TempDir     string
	VerifyMime  string
}

// ceiling is the most the whole body may occupy.
func (l Limits) ceiling() int64 {
	if l.MaxParts <= 0 {
		return l.MaxPartSize
	}
	return l.MaxPartSize * int64(l.MaxParts)
}

// Part is a buffered multipart file part. A part is claimed by at most one
// savefile rule per request.
type Part struct {
	FieldName string
	Filename  string
	Size      int64

	// MimeType is the type the part is routed by: the detected type whenever
	// the declaration cannot be trusted.
	MimeType string
	// DeclaredMimeType is what the client said, DetectedMimeType what the
	// leading bytes look like.
	DeclaredMimeType string
	DetectedMimeType string

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
	DeclaredMimeType string `json:"declared_mime_type"`
}

// FormState is the per-request parse result shared by every handler that takes
// part in form processing.
type FormState struct {
	Values url.Values
	Parts  []*Part
	Files  []SavedFileInfo

	limits    Limits
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
// still route the file parts. Bodies are only claimed when their media type is
// form-shaped; JSON and raw bodies are left for IngestJSON and IngestRawBody.
func Ensure(r *http.Request, limits Limits) (*FormState, error) {
	if state := FromRequest(r); state != nil {
		return state, nil
	}

	global := config.Global()
	if limits.MaxPartSize <= 0 {
		limits.MaxPartSize = global.MaxSize
	}
	if limits.MaxParts <= 0 {
		limits.MaxParts = global.MaxParts
	}
	if limits.TempDir == "" {
		limits.TempDir = global.TempDir
	}

	state := &FormState{Values: url.Values{}, limits: limits}

	var err error
	switch paths.NormalizeMime(r.Header.Get("Content-Type")) {
	case "multipart/form-data":
		form := r.MultipartForm
		switch {
		case r.ContentLength > limits.ceiling():
			err = errTooLarge(limits)
		case form != nil && (len(form.Value) > 0 || len(form.File) > 0):
			state.consumed = true
			err = state.adopt(form)
		case form != nil:
			// An earlier handler already consumed the multipart stream, leaving
			// an empty form behind. There is nothing left to read.
			state.consumed = true
		default:
			state.consumed = true
			err = state.consumeMultipart(r)
		}

	case "application/x-www-form-urlencoded":
		state.consumed = true
		err = state.ingestURLEncoded(r)

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

	return state, nil
}

// Cleanup removes every temp file that was not moved to its destination. It is
// idempotent, and callers are expected to defer it rather than hang it off the
// request context: http.Request.Context is canceled when the client closes the
// connection, which can happen while the parts are still being routed. Tying
// cleanup to it deleted temp files out from under an in-flight write.

func errTooLarge(limits Limits) error {
	return caddyhttp.Error(http.StatusRequestEntityTooLarge,
		fmt.Errorf("%w: body is limited to %d parts of %d bytes",
			paths.ErrTooLarge, limits.MaxParts, limits.MaxPartSize))
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

// adopt imports file parts that another module already parsed. The limits are
// enforced against what was parsed, since the read already happened.
func (st *FormState) adopt(form *multipart.Form) error {
	count := 0
	for _, vals := range form.Value {
		count += len(vals)
	}
	for _, headers := range form.File {
		count += len(headers)
	}
	if count > st.limits.MaxParts {
		return errTooLarge(st.limits)
	}

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
			if header.Size > st.limits.MaxPartSize {
				return caddyhttp.Error(http.StatusRequestEntityTooLarge, paths.ErrTooLarge)
			}
			declaredType, detectedType, routingType, err := st.classifyPart(
				header.Header.Get("Content-Type"),
				func() string { return sniffFileHeader(header) },
			)
			if err != nil {
				return err
			}

			st.Parts = append(st.Parts, &Part{
				FieldName:        field,
				Filename:         header.Filename,
				Size:             header.Size,
				MimeType:         routingType,
				DeclaredMimeType: declaredType,
				DetectedMimeType: detectedType,
				Header:           header,
			})
		}
	}
	return nil
}

// classifyPart records what was declared, sniffs the content, and resolves the
// type the part is routed by. A strict mismatch is rejected.
func (st *FormState) classifyPart(declared string, sniff func() string) (declaredType, detectedType, routingType string, err error) {
	declaredType = paths.NormalizeMime(declared)
	detectedType = paths.NormalizeMime(sniff())

	routingType, err = paths.ResolveMime(st.limits.VerifyMime, declaredType, detectedType)
	if err != nil {
		return "", "", "", caddyhttp.Error(http.StatusUnsupportedMediaType, err)
	}
	return declaredType, detectedType, routingType, nil
}

// sniffFileHeader reads the leading bytes of an already parsed part.
func sniffFileHeader(header *multipart.FileHeader) string {
	file, err := header.Open()
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	return paths.SniffMimeReader(file)
}
