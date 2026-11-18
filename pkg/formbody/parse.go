package formbody

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/paths"
)

func (st *FormState) consumeMultipart(r *http.Request) error {
	if err := paths.EnsureDir(st.tempDir); err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, fmt.Errorf("creating temp_dir: %w", err))
	}

	reader, err := r.MultipartReader()
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("reading multipart body: %w", err))
	}

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("reading multipart part: %w", err))
		}

		err = st.consumePart(part)
		part.Close()
		if err != nil {
			return err
		}
	}
}

func (st *FormState) consumePart(part *multipart.Part) error {
	name := part.FormName()
	if part.FileName() == "" {
		return st.consumeValue(part, name)
	}
	return st.consumeFile(part, name)
}

func (st *FormState) consumeValue(part *multipart.Part, name string) error {
	value, err := io.ReadAll(io.LimitReader(part, st.maxSize+1))
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, err)
	}
	if int64(len(value)) > st.maxSize {
		return caddyhttp.Error(http.StatusRequestEntityTooLarge, paths.ErrTooLarge)
	}

	st.Values.Add(name, string(value))
	return nil
}

func (st *FormState) consumeFile(part *multipart.Part, name string) error {
	temp, err := os.CreateTemp(st.tempDir, "caddy-savefile-*")
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	tempPath := temp.Name()
	st.tempPaths = append(st.tempPaths, tempPath)

	size, err := paths.CopyLimited(temp, part, st.maxSize)
	closeErr := temp.Close()
	if err != nil {
		if errors.Is(err, paths.ErrTooLarge) {
			return caddyhttp.Error(http.StatusRequestEntityTooLarge, err)
		}
		return caddyhttp.Error(http.StatusBadRequest, err)
	}
	if closeErr != nil {
		return caddyhttp.Error(http.StatusInternalServerError, closeErr)
	}

	mimeType := paths.NormalizeMime(part.Header.Get("Content-Type"))
	if mimeType == "" {
		mimeType = paths.SniffMimePath(tempPath)
	}

	st.Parts = append(st.Parts, &Part{
		FieldName: name,
		Filename:  part.FileName(),
		MimeType:  mimeType,
		Size:      size,
		TempPath:  tempPath,
	})
	return nil
}
