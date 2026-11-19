# `caddy-savefile` Technical Specification

> **Status:** this is the design document for the suite, kept in step with the
> implementation. Everything below describes what the code does today, except
> the items collected under "Not yet built" at the end, which are specified but
> deliberately unimplemented or blocked upstream. `README.md` is the user-facing
> guide; where this document and the code disagree, the code is right.

**Module IDs:**

* `http.handlers.savefile` (HTTP handler and Caddyfile directive for routing
  uploaded file parts to disk by MIME type)
* `http.handlers.formdata` (HTTP handler and Caddyfile directive for reading a
  form body into fields templates can use)
* `http.handlers.templates.functions.savefile` (Caddy `templates` extension that
  writes generated text or binary content to disk)
* global option `savefile` (shared storage limits; not a module)

**Purpose:** provide an end-to-end Caddy v2 form-processing, file-upload and
template-driven persistence suite: incoming parts are routed to disk by MIME
type, form fields (including JSON bodies) are parsed once for templates, and
templates can write their own output inside a confined directory.

---

## 1. Architecture & Execution Pipeline

The suite separates three concerns, plus the shared machinery under them:

1. **`formbody`** owns the single-read body: one `FormState` per request, shared
   by whichever directives take part. Multipart parts are buffered into
   `temp_dir`, so a later handler can still route them.
2. **`paths`** owns every security-relevant decision: filename sanitization,
   `root_dir` containment, UUID generation, MIME classification and matching,
   and copy/move/sniff helpers. It imports no other project package.
3. **`config`** owns the global block, the defaults, size parsing and the
   package-wide store the handlers fall back to.
4. **`formdata`**, **`savefile`** and the templates action are thin: they read
   their own configuration, ask `formbody` for the parse, and act on it.

Import direction is one-way: `config` and `paths` at the bottom, `formbody`
above them, then the three modules. Nothing lower imports anything higher.

Which directive claims the body depends on the media type; see section 4.

---

## 2. Global Configuration Block (`savefile`)

The global block defines the storage boundaries shared by the handlers and the
template action. Filename sanitization and `root_dir` containment are not
configurable; they are invariants (section 7).

```caddyfile
{
	savefile {
		root_dir /var/uploads
		max_size 50MB
		max_parts 200
		temp_dir /tmp/caddy-uploads
		verify_mime on
	}
}
```

### Global Options Reference

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `root_dir` | string | `./uploads` | Base directory for every write: HTTP uploads and template `savefile` calls. |
| `max_size` | size | `10MB` | Largest single file part, form field value, JSON body or template write. |
| `max_parts` | int | `100` | Largest number of parts in one multipart body, fields included. |
| `temp_dir` | string | OS temp dir | Scratch space while a body is read. |
| `verify_mime` | string | `on` | How strictly a declared MIME type is believed: `on`, `strict` or `off`. |

Sizes accept binary suffixes (`512KB`, `50MB`, `1GB`) or a bare byte count.

`root_dir` and `temp_dir` are created and write-probed during `Provision`, so a
typo or a read-only mount fails config load and reload instead of answering
`500` per request.

The block is Caddyfile-only. A JSON config has no global block, so each handler
carries its own fields, and `formdata` spells `max_size` as `max_memory`.

---

## 3. Caddyfile Directives

Both directives are registered with `RegisterHandlerDirective`, so each one
accepts an optional leading matcher token, and both are placed in the
`directiveOrder`: `formdata` after `request_body`, `savefile` before
`templates`. Neither directive does anything for a method other than `POST`,
`PUT` or `PATCH`.

### 3.1 `formdata` Directive

```text
formdata
```

Reads the request body once and publishes the submitted fields, with or without
any file upload rule in the route.

* Bodies understood: `application/x-www-form-urlencoded`, `multipart/form-data`,
  `application/json` and any `*/*+json` subtype. Anything else is left to
  `savefile`.
* Publishes `{http.vars.form.<key>}` placeholders (first value per key) and
  populates `r.PostForm` and `r.Form`, which templates read as
  `.Req.Form.Get "<key>"`. Body values come first, the URL query is merged after
  them.
* JSON is folded into dotted field names, because `url.Values` is flat; see
  section 4.
* Field limits for a JSON config only: `max_memory`, `max_parts`, `temp_dir`,
  `verify_mime`. In a Caddyfile the directive takes no arguments and no block, so
  a Caddyfile route always uses the global values.

### 3.2 `savefile` Directive (Multi-MIME Routing)

```text
savefile <mimetype_filter> <destination_template>
```

* **`<mimetype_filter>`:** an exact type (`application/pdf`), a wildcard subtype
  (`image/*`), or the catch-all `*/*`. Write `*/*`, not a bare `*`: the leading
  token of a handler directive is scanned for a matcher, so a bare `*` is taken
  as a catch-all request matcher and the rule ends up one argument short.
* **`<destination_template>`:** target path relative to `root_dir`, or absolute
  as long as it resolves inside `root_dir`. Supports `{uuid}` (a fresh version 4
  UUID per file part), `{filename}` (the client filename, sanitized) and any
  Caddy placeholder.
* Rules are tried in order and each part is claimed by at most one of them. A
  part that matches no rule is drained and discarded, so nothing unmatched is
  ever written into `root_dir`.
* The destination is resolved after the request replacer runs, then `{uuid}` and
  `{filename}` are expanded per part. The Caddyfile adapter rewrites the `{uuid}`
  shorthand into `{http.request.uuid}`, and the handler claims that form back so
  the UUID is per part rather than per request.
* Limits for a JSON config only: `root_dir`, `temp_dir`, `max_size`, `max_parts`,
  `verify_mime`.

#### Combined Caddyfile Example

```caddyfile
example.com {
	@submit {
		method POST
	}

	handle @submit {
		# parse form fields for templates
		formdata

		# route image uploads to the images directory
		savefile image/* images/{uuid}_{filename}

		# route PDF uploads to the documents directory
		savefile application/pdf docs/{uuid}_{filename}

		# any other type is drained and discarded

		respond "{{include \"/process.html\"}}" 200

		templates {
			extensions {
				savefile
			}
		}
	}
}
```

---

## 4. Body Handling

One body, read once. `Ensure` records what it claimed in `FormState.consumed`;
the two on-demand ingests are no-ops once that is set, so the order of the
directives in a chain does not matter.

| Content-Type | Claimed by | Result |
| --- | --- | --- |
| `multipart/form-data` | `Ensure`, from either directive | fields into `Values`, each file part buffered into `temp_dir` as a `Part` |
| `application/x-www-form-urlencoded` | `Ensure`, from either directive | `Values` via a bounded read, not `ParseForm` |
| `application/json`, `*/*+json` | `IngestJSON`, called by `formdata` | folded into `Values` |
| anything else | `IngestRawBody`, called by `savefile` | buffered as a single `Part` |

Consequences:

* JSON is form data, not a file, on a route that has `formdata`. Leave
  `formdata` out of the route to store the raw JSON.
* A plain POST of one file works on any route with a rule: the whole body becomes
  a part whose filename comes from `Content-Disposition`, else the last path
  segment, else `upload`, given an extension derived from the resolved type when
  the name has none. The pinned table is
  `gz`, `json`, `pdf`, `zip`, `gif`, `jpg`, `png`, `svg`, `webp`, `csv`, `html`,
  `txt`, `mp4`; every other type falls back to the first extension the standard
  library reports.
* The limits come from whichever directive creates the state, so two directives
  with different `max_parts` mean the first one in the chain wins.

JSON flattening, since `url.Values` is flat:

| JSON | Result |
| --- | --- |
| `{"address": {"city": "x"}}` | `address.city` |
| `{"tags": ["a", "b"]}` | `Form["tags"]` holds both; `{http.vars.form.tags}` is `a` |
| `{"items": [{"name": "n"}]}` | `items.0.name` |
| `{"count": 1000000000}` | `1000000000`, literal text, no float rounding |
| `{"missing": null}` | an empty value |

### 4.1 Placeholders

| Placeholder | Description |
| --- | --- |
| `{http.vars.form.<key>}` | first submitted value for form field `<key>` |
| `{http.vars.savefile.filename}` | sanitized client filename of the file saved most recently |
| `{http.vars.savefile.saved_path}` | that file's destination, relative to `root_dir` |
| `{http.vars.savefile.filesize}` | bytes written, as a decimal number |
| `{http.vars.savefile.mimetype}` | the MIME type the bytes were detected as |

The vars table is flat, so every key becomes its own placeholder rather than a
branch of one tree, and a map stored under one key would not create child
placeholders. A request that writes several files therefore leaves the metadata
of the last one written. The `{{ savefile }}` action cannot publish them, because
extension functions receive no request; it returns the path it wrote instead.

The handler also records every saved file internally as `SavedFileInfo`:

```go
type SavedFileInfo struct {
	FieldName        string // form input name, or "body" for a raw body
	OriginalFilename string // sanitized client filename
	SavedPath        string // final path on disk
	Filesize         int64  // bytes written
	MimeType         string // detected type
	DeclaredMimeType string // what the client claimed
}
```

---

## 5. Caddy Templates (`templates`) Integration

### 5.1 Data Available To Templates

Templates reach form data through the request itself: `formdata` populates
`r.Form`/`r.PostForm`, so a template uses `{{.Req.Form.Get "username"}}`, or
`{{placeholder "http.vars.form.username"}}`. Template extension functions
receive no request and no template context, so a per-request dot such as
`.FormData` is not available; see section 8.

The `templates` handler reads `.Req` as the live `*http.Request`, which is why
populating the standard library's form fields is the whole integration.

### 5.2 Template Function: `{{ savefile filename content }}`

Enable it under the templates handler:

```caddyfile
templates {
	extensions {
		savefile
	}
}
```

```text
{{ savefile <filename> <content> }}
```

* **`filename` (`string`):** target path inside `root_dir`. Every segment is
  sanitized and the resolved path is confined to `root_dir`.
* **`content` (`string` | `[]byte` | `io.Reader`):** payload to write. Strings
  are written as UTF-8 bytes, `[]byte` and `io.Reader` values verbatim, with no
  encoding changes and no newline translation. String-kinded template types such
  as `template.HTML` work too.
* **Return value (`string`):** the path relative to `root_dir`. Escaping
  `root_dir` or exceeding `max_size` fails template execution.
* Configuration (JSON config only): `root_dir`, `max_size`, `temp_dir`, under
  the field Caddy names `match`, which is the key the templates handler uses for
  its extension map.

#### Complete Template Example (`process.html`)

```html
<!DOCTYPE html>
<html>
<head><title>Submission Processed</title></head>
<body>
	{{/* form fields come from the request */}}
	{{$user := .Req.Form.Get "username"}}
	{{$notes := .Req.Form.Get "notes"}}

	<h1>Submission Received from {{$user}}</h1>

	{{/* write generated content from the template itself */}}
	{{$receipt := savefile (printf "receipts/%s-notes.txt" $user) $notes}}
	<p>Saved notes to: <code>{{$receipt}}</code></p>
</body>
</html>
```

---

## 6. MIME Verification

`paths.ResolveMime` is the single place that decides which type a part is routed
by, and it is the only reason a client's `Content-Type` is not simply believed.

* The **declaration decides**, because sniffing is a magic-number heuristic: a
  real `.xlsx` sniffs as `application/zip`, so routing everything by content
  would break fine-grained rules.
* A missing or `application/octet-stream` declaration defers to the content.
* **Active content never passes under a disagreeing declaration.** `text/html`,
  `text/xml`, `application/xml`, `image/svg+xml`, `application/javascript`,
  `text/javascript` and anything `+xml` must match a rule for the detected type,
  so a page claiming `image/png` cannot be stored under `image/*`.
* `verify_mime strict` rejects any mismatch with `415`; `off` trusts the client.
  It is a global option only; there is no per-directive Caddyfile syntax.

`Part` carries all three types: `MimeType` (the routing key),
`DeclaredMimeType` and `DetectedMimeType`, so downstream consumers never get a
lie about what was stored.

---

## 7. Security Invariants & Error Handling

* **Mandatory filename sanitization:** applied unconditionally, including
  `{filename}` in a destination and the `filename` argument to the template
  action. Traversal is *stripped*, not merely rejected; Unicode is normalized.
* **Strict `root_dir` containment:** every write path is resolved and checked
  against `root_dir` with `filepath.Rel`. An escape is `400 Bad Request` in a
  handler and a template error in the action.
* **Bounded resource use:** every part is capped by `max_size` and the number of
  parts by `max_parts`, fields included, so disk and memory use are bounded by
  construction rather than by trusting the client. Nothing is read past the
  limit: a declared `Content-Length` over the ceiling, `max_size × max_parts`, is
  refused before the body is touched.
* **Payload size enforcement:** exceeding `max_size` in either an upload or a
  template write halts the write, deletes any partial file, and answers
  `413 Payload Too Large`.
* **Safe discard:** parts matching no rule are drained and discarded, so the
  stream stays aligned and nothing unapproved is written into `root_dir`.
* **Binary integrity:** payloads are written verbatim, with no string coercion
  and no newline translation.
* **Handler errors** go through `caddyhttp.Error`, so Caddy renders the status.
* **Temp file lifetime:** temp files are released by the directives' own
  `defer state.Cleanup()`, never by `context.AfterFunc` on the request. A request
  context is canceled when the client closes the connection, which can happen
  while parts are still being routed; hanging cleanup off it deleted temp files
  out from under an in-flight write.

| Situation | Result |
| --- | --- |
| part larger than `max_size` | `413`, partial file removed |
| more than `max_parts` parts | `413` |
| declared `Content-Length` over the ceiling | `413` before reading |
| destination resolving outside `root_dir` | `400` |
| malformed body | `400` |
| `verify_mime strict` mismatch | `415` |
| unusable `root_dir` or `temp_dir` | config load fails |
| method other than `POST`, `PUT`, `PATCH` | nothing parsed or written |

---

## 8. Not yet built

The live list is `BACKLOG.md`; this section keeps the design detail for the
items that are still missing.

* **`.FormData` and `.Savefile` on the template dot**, with the Go types
  `TemplateContext` and `SavefileContext`. The stock `templates` handler gives
  extension functions no request and no template context, and it controls the
  execution dot, so this needs an upstream change. Templates get `.Req.Form`
  (the standard library's `url.Values`), `{http.vars.form.*}` and
  `{http.vars.savefile.*}` instead.

Per-route limits and a per-file view of saved metadata are deliberately out of
scope. A JSON config already sets every limit per handler instance, and the
single flat `{http.vars.savefile.*}` set covers the common one-file request.

Two additions beyond the original design, now part of it: JSON bodies (folded by
`formdata`, else stored raw by `savefile`), and the `max_parts` and `verify_mime`
options with the raw single-body upload path.
