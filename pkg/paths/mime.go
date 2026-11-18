package paths

import (
	"errors"
	"fmt"
	"strings"
)

// MIME verification modes, as configured by the verify_mime option.
const (
	VerifyMimeOff    = "off"
	VerifyMimeOn     = "on"
	VerifyMimeStrict = "strict"
)

// ErrMimeMismatch reports a declared type that strict verification rejects.
var ErrMimeMismatch = errors.New("declared MIME type does not match the content")

// ValidVerifyMime reports whether mode is a recognized verify_mime value.
func ValidVerifyMime(mode string) bool {
	switch mode {
	case VerifyMimeOff, VerifyMimeOn, VerifyMimeStrict:
		return true
	}
	return false
}

// IsActiveContent reports whether bytes of this type can execute or be reparsed
// as markup, making it unsafe to let a client's declaration route them.
func IsActiveContent(mimeType string) bool {
	switch mimeType {
	case "text/html",
		"text/xml",
		"application/xml",
		"application/xhtml+xml",
		"image/svg+xml",
		"application/javascript",
		"text/javascript":
		return true
	}
	return strings.HasSuffix(mimeType, "+xml")
}

// ResolveMime decides which media type a part is routed by, given what the client
// declared and what the bytes look like.
//
// The declaration decides, because sniffing is a magic-number heuristic that
// reports an .xlsx as application/zip. Two exceptions: a missing or generic
// declaration defers to the content, and active content is never allowed to pass
// under a declaration that disagrees with it, so a page claiming image/png
// cannot match an image/* rule.
//
// Off trusts the declaration outright. Strict requires it to equal the detected
// type, and returns ErrMimeMismatch when it does not.
func ResolveMime(mode, declared, detected string) (string, error) {
	declared = NormalizeMime(declared)
	detected = NormalizeMime(detected)

	switch {
	case declared == "" && detected == "":
		return "application/octet-stream", nil
	case declared == "":
		return detected, nil
	case detected == "":
		return declared, nil
	}

	switch mode {
	case VerifyMimeOff:
		return declared, nil

	case VerifyMimeStrict:
		if declared != detected {
			return declared, fmt.Errorf("%w: declared %s, content is %s",
				ErrMimeMismatch, declared, detected)
		}
		return declared, nil
	}

	if declared == "application/octet-stream" || IsActiveContent(detected) {
		return detected, nil
	}
	return declared, nil
}
