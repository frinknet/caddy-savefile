// Package savefile implements the `savefile` directive, which routes uploaded
// file parts to their destination by MIME type inside a confined root
// directory. Only POST, PUT and PATCH are handled; every other method passes
// through untouched.
package savefile

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"github.com/frinknet/caddy-savefile/pkg/config"
	"github.com/frinknet/caddy-savefile/pkg/formbody"
	"github.com/frinknet/caddy-savefile/pkg/paths"
)

func init() {
	caddy.RegisterModule(SaveFile{})
}

// SaveFile routes matching parts of a multipart/form-data submission to disk
// and silently discards the rest.
type SaveFile struct {
	// MimeFilter is an exact MIME type, a wildcard subtype such as image/*, or
	// the catch-all *.
	MimeFilter string `json:"mime_filter,omitempty"`

	// Destination is the target path, relative to root_dir or absolute within
	// it. It supports the {uuid} and {filename} tokens, where {uuid} is a fresh
	// version 4 UUID per file part, as well as any Caddy placeholder. The
	// Caddyfile adapter rewrites the {uuid} shorthand into
	// {http.request.uuid}, which the handler treats as its own token so that
	// parts sharing a filename cannot overwrite one another.
	Destination string `json:"destination,omitempty"`

	// RootDir is the base directory for saved files.
	RootDir string `json:"root_dir,omitempty"`

	// TempDir is the scratch directory used while reading the multipart stream.
	TempDir string `json:"temp_dir,omitempty"`

	// MaxSize is the largest accepted file part, in bytes.
	MaxSize int64 `json:"max_size,omitempty"`

	logger *zap.Logger

	// MaxParts caps the number of parts in one multipart body.
	MaxParts int `json:"max_parts,omitempty"`

	// VerifyMime controls how a declared MIME type is treated.
	VerifyMime string `json:"verify_mime,omitempty"`
}

// CaddyModule returns the Caddy module information.
func (SaveFile) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.savefile",
		New: func() caddy.Module { return new(SaveFile) },
	}
}

// Provision fills unset fields from the global savefile options.
func (sf *SaveFile) Provision(ctx caddy.Context) error {
	sf.logger = ctx.Logger()
	global := config.Global()
	if sf.RootDir == "" {
		sf.RootDir = global.RootDir
	}
	if sf.TempDir == "" {
		sf.TempDir = global.TempDir
	}
	if sf.MaxSize <= 0 {
		sf.MaxSize = global.MaxSize
	}
	if sf.MaxParts <= 0 {
		sf.MaxParts = global.MaxParts
	}
	if sf.VerifyMime == "" {
		sf.VerifyMime = global.VerifyMime
	}

	// Fail at config load rather than 500ing on every upload: a missing mount or
	// a read-only filesystem shows up here, where the operator will see it.
	if err := paths.ProbeWritable(sf.RootDir, 0o755); err != nil {
		return fmt.Errorf("savefile: root_dir %q is unusable: %w", sf.RootDir, err)
	}
	if err := paths.ProbeWritable(sf.TempDir, 0o700); err != nil {
		return fmt.Errorf("savefile: temp_dir %q is unusable: %w", sf.TempDir, err)
	}
	return nil
}

// Validate ensures the directive has a MIME filter and a destination.
func (sf SaveFile) Validate() error {
	if sf.MimeFilter == "" {
		return errors.New("savefile: a MIME type filter is required")
	}
	if sf.Destination == "" {
		return errors.New("savefile: a destination is required")
	}
	return nil
}

// ServeHTTP writes every unclaimed part whose MIME type matches the filter.
func (sf SaveFile) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return next.ServeHTTP(w, r)
	}

	state, err := formbody.Ensure(r, formbody.Limits{
		MaxPartSize: sf.MaxSize,
		MaxParts:    sf.MaxParts,
		TempDir:     sf.TempDir,
		VerifyMime:  sf.VerifyMime,
	})
	if err != nil {
		return err
	}
	defer state.Cleanup()
	if err := state.IngestRawBody(r); err != nil {
		return err
	}

	for _, part := range state.Parts {
		if part.Saved {
			continue
		}

		// A declaration that lost to the content is worth saying out loud, whether
		// or not one of the rules ends up matching it.
		if part.DeclaredMimeType != "" && part.DeclaredMimeType != part.MimeType {
			sf.log().Warn("routing a part by its content, not its declaration",
				zap.String("filename", part.Filename),
				zap.String("declared_mime", part.DeclaredMimeType),
				zap.String("detected_mime", part.DetectedMimeType),
			)
		}

		if !paths.MimeMatches(sf.MimeFilter, part.MimeType) {
			sf.log().Debug("discarded part",
				zap.String("field", part.FieldName),
				zap.String("filename", part.Filename),
				zap.String("declared_mime", part.DeclaredMimeType),
				zap.String("detected_mime", part.DetectedMimeType),
				zap.String("filter", sf.MimeFilter),
			)
			continue
		}

		info, err := sf.savePart(r, part)
		if err != nil {
			return err
		}

		part.Saved = true
		state.Files = append(state.Files, info)

		sf.log().Debug("saved part",
			zap.String("field", info.FieldName),
			zap.String("filename", info.OriginalFilename),
			zap.String("path", info.SavedPath),
			zap.Int64("bytes", info.Filesize),
			zap.String("mime", info.MimeType),
			zap.String("declared_mime", info.DeclaredMimeType),
		)
	}

	return next.ServeHTTP(w, r)
}

// log returns the provisioned logger, or a no-op one when Provision has not run
// (as in unit tests).
func (sf SaveFile) log() *zap.Logger {
	if sf.logger == nil {
		return zap.NewNop()
	}
	return sf.logger
}

func (sf SaveFile) savePart(r *http.Request, part *formbody.Part) (formbody.SavedFileInfo, error) {
	filename := paths.SanitizeFilename(part.Filename)

	destination, _, err := paths.ResolveDestination(sf.RootDir, sf.expandDestination(r), paths.NewUUID(), filename)
	if err != nil {
		return formbody.SavedFileInfo{}, caddyhttp.Error(http.StatusBadRequest, err)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return formbody.SavedFileInfo{}, caddyhttp.Error(http.StatusInternalServerError, err)
	}

	if err := sf.writePart(part, destination); err != nil {
		return formbody.SavedFileInfo{}, err
	}

	size := part.Size
	if info, err := os.Stat(destination); err == nil {
		size = info.Size()
	}

	// Record what the bytes are, not what the client claimed, and keep the
	// declaration alongside it.
	detected := part.DetectedMimeType
	if detected == "" {
		detected = part.MimeType
	}

	return formbody.SavedFileInfo{
		FieldName:        part.FieldName,
		OriginalFilename: filename,
		SavedPath:        destination,
		Filesize:         size,
		MimeType:         detected,
		DeclaredMimeType: part.DeclaredMimeType,
	}, nil
}

// expandDestination resolves the configured destination template, expanding any
// Caddy placeholders the request can satisfy. The {uuid} shorthand is claimed
// back from its canonical form first, because the UUID has to be unique per file
// part rather than per request.
func (sf SaveFile) expandDestination(r *http.Request) string {
	destination := strings.ReplaceAll(sf.Destination, "{http.request.uuid}", "{uuid}")
	if repl := formbody.Replacer(r); repl != nil {
		destination = repl.ReplaceKnown(destination, "")
	}
	return destination
}

func (sf SaveFile) writePart(part *formbody.Part, destination string) error {
	if part.TempPath != "" {
		if err := paths.MoveFile(part.TempPath, destination); err != nil {
			return caddyhttp.Error(http.StatusInternalServerError, err)
		}
		part.TempPath = ""
		return nil
	}

	source, err := part.Open()
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	defer source.Close()

	if err := paths.CopyFile(destination, source, config.MaxSizeOr(sf.MaxSize)); err != nil {
		if errors.Is(err, paths.ErrTooLarge) {
			return caddyhttp.Error(http.StatusRequestEntityTooLarge, err)
		}
		return caddyhttp.Error(http.StatusInternalServerError, fmt.Errorf("saving %s: %w", destination, err))
	}
	return nil
}
