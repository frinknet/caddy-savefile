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
	if err := paths.EnsureDir(st.limits.TempDir, 0o700); err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, fmt.Errorf("creating temp_dir: %w", err))
	}

	reader, err := r.MultipartReader()
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("reading multipart body: %w", err))
	}

	parts := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("reading multipart part: %w", err))
		}

		parts++
		if parts > st.limits.MaxParts {
			part.Close()
			return errTooLarge(st.limits)
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
	value, err := io.ReadAll(io.LimitReader(part, st.limits.MaxPartSize+1))
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, err)
	}
	if int64(len(value)) > st.limits.MaxPartSize {
		return caddyhttp.Error(http.StatusRequestEntityTooLarge, paths.ErrTooLarge)
	}

	st.Values.Add(name, string(value))
	return nil
}

func (st *FormState) consumeFile(part *multipart.Part, name string) error {
	temp, err := os.CreateTemp(st.limits.TempDir, "caddy-savefile-*")
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	tempPath := temp.Name()
	st.tempPaths = append(st.tempPaths, tempPath)

	size, err := paths.CopyLimited(temp, part, st.limits.MaxPartSize)
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

	declaredType, detectedType, routingType, err := st.classifyPart(
		part.Header.Get("Content-Type"),
		func() string { return paths.SniffMimePath(tempPath) },
	)
	if err != nil {
		return err
	}

	st.Parts = append(st.Parts, &Part{
		FieldName:        name,
		Filename:         part.FileName(),
		Size:             size,
		MimeType:         routingType,
		DeclaredMimeType: declaredType,
		DetectedMimeType: detectedType,
		TempPath:         tempPath,
	})
	return nil
}
