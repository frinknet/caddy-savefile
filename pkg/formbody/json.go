package formbody

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/frinknet/caddy-savefile/pkg/paths"
)

// IngestJSON folds an application/json body into form values. Object keys
// become dotted paths, an array of scalars becomes repeated values for its key,
// and an array holding anything structured is indexed. Bodies already read by a
// form parse are left alone.
func (st *FormState) IngestJSON(r *http.Request) error {
	if st.consumed || !isJSONMediaType(paths.NormalizeMime(r.Header.Get("Content-Type"))) {
		return nil
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, st.maxSize+1))
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("reading JSON body: %w", err))
	}
	if int64(len(raw)) > st.maxSize {
		return caddyhttp.Error(http.StatusRequestEntityTooLarge, paths.ErrTooLarge)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("parsing JSON body: %w", err))
	}

	flattenJSON("", payload, st.Values)
	st.consumed = true
	st.attachForm(r)

	return nil
}

func isJSONMediaType(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func flattenJSON(prefix string, value any, out url.Values) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			flattenJSON(joinJSONKey(prefix, key), typed[key], out)
		}

	case []any:
		if prefix != "" && scalarsOnly(typed) {
			for _, item := range typed {
				out.Add(prefix, jsonScalar(item))
			}
			return
		}
		for i, item := range typed {
			flattenJSON(joinJSONKey(prefix, strconv.Itoa(i)), item, out)
		}

	default:
		if prefix == "" {
			return
		}
		out.Add(prefix, jsonScalar(value))
	}
}

func joinJSONKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func scalarsOnly(items []any) bool {
	for _, item := range items {
		switch item.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

func jsonScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}
