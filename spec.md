# `caddy-savefile` Technical Specification

> **Status:** original design document, kept for reference. Where it and the
> implementation disagree, the code is right; the differences are listed under
> "Known deviations from `spec.md`" in `AGENTS.md`, and `README.md` documents
> current behaviour.

**Module IDs:**

* `http.handlers.savefile` (HTTP handler & Caddyfile directive for streaming multipart/form uploads)
* `http.handlers.formdata` (HTTP handler & Caddyfile directive for parsing URL-encoded and multipart form fields into template context)
* Template Function: `savefile` (Caddy `templates` extension for writing generated text or binary content directly to disk)

**Purpose:** Provide an end-to-end Caddy v2 form-processing, file-upload, and template-driven persistence suite—allowing incoming multipart uploads to be routed by MIME type, standard form fields to be parsed independently into template variables, and templates themselves to write text or binary payloads to disk safely.

---

## 1. Architecture & Execution Pipeline

The suite decouples upload streaming, form-field parsing, and template-initiated file writing into three composable primitives:

1. **`formdata` Directive (`http.handlers.formdata`):** Parses `application/x-www-form-urlencoded` and `multipart/form-data` text fields into Caddy's request context (`{http.vars.formdata.*}` and `.FormData` in templates) without requiring a file upload rule.
2. **`savefile` Directive (`http.handlers.savefile`):** Streams incoming `multipart/form-data` payloads via `multipart.Reader`, filters file parts against ordered MIME rules, unconditionally sanitizes filenames, generates server-side UUIDs, writes matched binary/text streams to disk, and silently discards unmatched file parts.
3. **`{{ savefile filename content }}` Template Action:** Registers a custom template function inside Caddy's `templates` engine, enabling templates to persist dynamically generated strings, JSON/CSV exports, or raw `[]byte` binary data directly within `root_dir`.

---

## 2. Global Configuration Block (`savefile`)

The global block defines shared storage boundaries, upload quotas, and scratch directories used by both the `savefile` HTTP handler and the `{{ savefile }}` template function. Filename sanitization and `root_dir` containment are permanently enforced as security invariants.

```caddyfile
{
    savefile {
        root_dir /var/uploads
        max_size 50MB
        temp_dir /tmp/caddy-uploads
    }
}

```

### Global Options Reference

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `root_dir` | string | `./uploads` | Base directory for all saved files (both HTTP uploads and template `savefile` calls). |
| `max_size` | string | `10MB` | Maximum allowed size per individual file part or template write (e.g., `50MB`, `1GB`). |
| `temp_dir` | string | OS Temp Dir | Scratch directory used during multipart stream processing. |

---

## 3. Caddyfile Directives

### 3.1 `formdata` Directive

Parses incoming form submissions (`application/x-www-form-urlencoded` or `multipart/form-data`) so form fields are immediately accessible in Caddy templates and placeholders, even on routes that do not accept file uploads.

#### Syntax

```text
formdata

```

#### Behavior

* Reads form key-value pairs up to the configured global `max_size` memory threshold.
* Populates request placeholders (`{http.vars.formdata.<field_name>}`) and exposes `.FormData` in Caddy templates.
* If `savefile` also runs in the same handler chain, `formdata` and `savefile` share the parsed form context so the request stream is never consumed twice.

---

### 3.2 `savefile` Directive (Multi-MIME Routing)

Routes matching file parts from a multipart submission to designated destinations while discarding unhandled MIME types.

#### Syntax

```text
savefile <mimetype_filter> <destination_template>

```

* **`<mimetype_filter>`:** Exact MIME type (e.g., `application/pdf`), wildcard subtype (e.g., `image/*`), or catch-all (`*`). Unmatched MIME types are safely drained and discarded.
* **`<destination_template>`:** Target path relative to `root_dir` (or absolute path within `root_dir`) supporting `{uuid}` and `{filename}`.

#### Combined Caddyfile Example

```caddyfile
example.com {
    @submit {
        method POST
    }

    handle @submit {
        # Parse standard form fields into .FormData for templates
        formdata

        # Route image uploads to the images directory
        savefile image/* /var/uploads/images/{uuid}_{filename}

        # Route PDF uploads to the documents directory
        savefile application/pdf /var/uploads/docs/{uuid}_{filename}

        # Any other uploaded MIME types (e.g., video/mp4) are discarded

        templates
        respond 200
    }
}

```

---

## 4. Placeholders & Data Sources

### Pre-Save Destination Tokens (Used in `savefile` Directive)

| Token | Source | Behavior |
| --- | --- | --- |
| `{filename}` | Client `Content-Disposition` header | Extracted from the multipart header and **unconditionally sanitized** (directory paths, `..`, null bytes, and unsafe characters are stripped; Unicode is normalized). |
| `{uuid}` | Server (`crypto/rand`) | Programmatically generated UUID v4 per file part to prevent filename collisions and overwrites. |

### Post-Save & Form Request Placeholders

| Placeholder | Description |
| --- | --- |
| `{http.vars.formdata.<key>}` | First submitted value for form field `<key>`. |
| `{http.vars.savefile.filename}` | Sanitized original filename of the last saved file. |
| `{http.vars.savefile.saved_path}` | Final resolved filesystem path where the file was written. |
| `{http.vars.savefile.filesize}` | Exact byte count written to disk. |
| `{http.vars.savefile.mimetype}` | Verified MIME type of the saved file. |

---

## 5. Caddy Templates (`templates`) Integration

### 5.1 Template Context Data

Both `.FormData` (from `formdata`) and `.Savefile` (from `savefile`) are injected into the template execution dot context:

```go
type TemplateContext struct {
    FormData url.Values      // map[string][]string of all parsed form fields
    Savefile SavefileContext // Metadata for all saved files in the request
}

type SavefileContext struct {
    Files []SavedFileInfo
}

type SavedFileInfo struct {
    FieldName        string // Form input name attribute
    OriginalFilename string // Sanitized client filename
    SavedPath        string // Final path on disk
    Filesize         int64  // Size in bytes
    MimeType         string // Verified MIME type
}

```

---

### 5.2 Template Function: `{{ savefile filename content }}`

Allows a Caddy template to write text or binary data directly to disk within `root_dir` during template evaluation.

#### Signature

```text
{{ savefile <filename> <content> }}

```

* **`filename` (`string`):** Target filename or relative subpath inside `root_dir` (e.g., `"reports/summary.txt"` or `printf "%s.json" $id`). Every path segment is unconditionally sanitized, and the resolved path is strictly confined within `root_dir`.
* **`content` (`string` | `[]byte` | `io.Reader`):** Payload to write. Strings are written as UTF-8 bytes; `[]byte` and `io.Reader` values (such as decoded base64 payloads or binary buffers) are written verbatim with zero encoding alteration.
* **Return Value (`string`):** Returns the final resolved relative path of the saved file (or triggers a template execution error if `max_size` is exceeded or path containment fails).

#### Complete Template Example (`process.html`)

```html
<!DOCTYPE html>
<html>
<head><title>Submission Processed</title></head>
<body>
    {{/* 1. Access parsed form fields via .FormData */}}
    {{$user := .FormData.Get "username"}}
    {{$notes := .FormData.Get "notes"}}

    <h1>Submission Received from {{$user}}</h1>

    {{/* 2. Persist generated text or binary content directly from the template */}}
    {{$receiptPath := savefile (printf "receipts/%s-notes.txt" $user) $notes}}
    <p>Saved form notes to: <code>{{$receiptPath}}</code></p>

    {{/* 3. List multipart files handled by the savefile directive */}}
    <h2>Uploaded Attachments ({{len .Savefile.Files}})</h2>
    <ul>
    {{range .Savefile.Files}}
        <li>
            <strong>{{.OriginalFilename}}</strong> ({{.MimeType}}, {{.Filesize}} bytes)
            <br>Stored at: <code>{{.SavedPath}}</code>
        </li>
    {{else}}
        <li>No matching attachments uploaded (unhandled MIME types were discarded).</li>
    {{end}}
    </ul>
</body>
</html>

```

---

## 6. Security Invariants & Error Handling

* **Mandatory Filename Sanitization:** Sanitization is hardcoded and applies equally to `{filename}` in the `savefile` directive and the `filename` argument in `{{ savefile filename content }}`. Directory traversal segments (`..`), null bytes, and invalid path characters are stripped unconditionally.
* **Strict `root_dir` Containment:** Every write path is resolved via `filepath.Clean` and verified against `root_dir` using `filepath.Rel`. Attempts to escape `root_dir` abort immediately (`400 Bad Request` in handlers; template execution error in `{{ savefile }}`).
* **Payload Size Enforcement:** Exceeding `max_size` in either multipart uploads or template `savefile` calls halts the write immediately, deletes any partial file on disk, and returns `413 Payload Too Large`.
* **Safe Discard of Unhandled Parts:** Unmatched MIME types in multipart requests are drained via `io.Copy(io.Discard, part)` so the stream remains aligned without writing unapproved files to disk.
* **Binary Integrity:** Both the HTTP handler (`io.Copy`) and template function (`[]byte` / `io.Reader` support) write raw bytes directly without string coercion or newline translation.
