package formbody

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/paths"
)

// commonExtensions names the file extension to use when a body arrives without
// one. The stdlib's table also carries oddities such as .ehtml for text/html,
// so the common types are pinned and everything else falls back to the first
// extension the stdlib reports.
var commonExtensions = map[string]string{
	"application/gzip": ".gz",
	"application/json": ".json",
	"application/pdf":  ".pdf",
	"application/zip":  ".zip",
	"image/gif":        ".gif",
	"image/jpeg":       ".jpg",
	"image/png":        ".png",
	"image/svg+xml":    ".svg",
	"image/webp":       ".webp",
	"text/csv":         ".csv",
	"text/html":        ".html",
	"text/plain":       ".txt",
	"video/mp4":        ".mp4",
}

// IngestRawBody buffers an unclaimed request body as a single file part, so a
// plain POST of one file can be routed by MIME type like any other upload. The
// part stays unclaimed until a savefile rule matches it. Bodies that belong to
// a form parse are left alone.
func (st *FormState) IngestRawBody(r *http.Request) error {
	if st.consumed || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return nil
	}
	if err := paths.EnsureDir(st.tempDir); err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, fmt.Errorf("creating temp_dir: %w", err))
	}

	temp, err := os.CreateTemp(st.tempDir, "caddy-savefile-*")
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	tempPath := temp.Name()
	st.tempPaths = append(st.tempPaths, tempPath)

	size, err := paths.CopyLimited(temp, r.Body, st.maxSize)
	closeErr := temp.Close()
	if err != nil {
		os.Remove(tempPath)
		if errors.Is(err, paths.ErrTooLarge) {
			return caddyhttp.Error(http.StatusRequestEntityTooLarge, err)
		}
		return caddyhttp.Error(http.StatusBadRequest, err)
	}
	if closeErr != nil {
		os.Remove(tempPath)
		return caddyhttp.Error(http.StatusInternalServerError, closeErr)
	}

	mimeType := paths.NormalizeMime(r.Header.Get("Content-Type"))
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = paths.SniffMimePath(tempPath)
	}

	st.consumed = true
	st.Parts = append(st.Parts, &Part{
		FieldName: "body",
		Filename:  rawFilename(r, mimeType),
		MimeType:  mimeType,
		Size:      size,
		TempPath:  tempPath,
	})

	return nil
}

// rawFilename names a body that arrived without multipart part headers: the
// Content-Disposition filename if the client sent one, otherwise the last
// segment of the request path, given an extension derived from the MIME type
// when the name has none.
func rawFilename(r *http.Request, mimeType string) string {
	if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil {
		if name := params["filename"]; name != "" {
			return name
		}
	}

	name := "upload"
	if r.URL != nil {
		if base := path.Base(r.URL.Path); base != "/" && base != "." && base != "" {
			name = base
		}
	}

	if path.Ext(name) == "" {
		name += mimeExtension(mimeType)
	}

	return name
}

func mimeExtension(mimeType string) string {
	if extension, ok := commonExtensions[mimeType]; ok {
		return extension
	}

	extensions, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(extensions) == 0 {
		return ""
	}
	sort.Strings(extensions)
	return extensions[0]
}
