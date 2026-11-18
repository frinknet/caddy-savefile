# caddy-savefile

A Caddy v2 plugin suite for handling form submissions and file uploads. It parses
form bodies into fields templates can read, routes uploaded files to disk by MIME
type, and lets templates write their own output inside a confined directory.

What it guarantees:

- **Nothing lands outside `root_dir`.** Every path is cleaned and checked with
  `filepath.Rel` before a write.
- **Filenames are sanitized unconditionally**, including client filenames used in
  a destination template and the `filename` argument to the template action.
  Traversal segments are stripped, not merely rejected.
- **A declared MIME type is not trusted blindly.** Active content such as HTML or
  SVG cannot be smuggled under an `image/*` rule.
- **Resource use is bounded.** Every part is capped by `max_size`, the number of
  parts by `max_parts`, and a body that declares a larger `Content-Length` is
  refused before it is read.

## Modules

| Module ID | Directive | Purpose |
| --- | --- | --- |
| `http.handlers.formdata` | `formdata` | parse form bodies into fields |
| `http.handlers.savefile` | `savefile` | route uploads to disk by MIME type |
| `http.handlers.templates.functions.savefile` | (templates extension) | the `{{ savefile }}` action |
| global option `savefile` | (global block) | shared storage limits |

## Install

```sh
xcaddy build --with github.com/frinknet/caddy-savefile
```

The repo also builds a scratch image, tagged `caddy-savefile:test`, with
`./build.sh`. That image ships `www/` as its `/etc/caddy/Caddyfile`, so it boots
into the example below.

## Quick start

```caddyfile
{
	savefile {
		root_dir /srv/www/uploads
		max_size 10MB
		temp_dir /tmp
	}
}

example.com {
	root * /srv/www
	encode gzip

	handle /upload* {
		formdata

		savefile image/* images/{uuid}_{filename}
		savefile application/pdf docs/{uuid}_{filename}

		respond "{{include \"/process.html\"}}" 200

		templates {
			extensions {
				savefile
			}
		}
	}

	file_server
}
```

`www/` holds this configuration plus `index.html` (the form) and `process.html`
(the result page).

A POST to `/upload` therefore parses the form fields, stores each `image/*` part
under `images/` and each `application/pdf` part under `docs/`, discards every
other part, and renders `process.html` with the fields available.

## Global options

```caddyfile
{
	savefile {
		root_dir /srv/www/uploads
		max_size 50MB
		max_parts 200
		temp_dir /tmp/caddy-uploads
		verify_mime on
	}
}
```

| Option | Default | Meaning |
| --- | --- | --- |
| `root_dir` | `./uploads` | base directory for every write |
| `max_size` | `10MB` | largest file part, field value, JSON body or template write |
| `max_parts` | `100` | largest number of parts in one multipart body, fields included |
| `temp_dir` | OS temp dir | scratch space while reading a body |
| `verify_mime` | `on` | how strictly a declared type is believed |

Sizes accept binary suffixes (`512KB`, `50MB`, `1GB`) or a bare byte count.

`max_size` is not only a file ceiling. It also bounds a single form field value,
a whole JSON body, and a template write. `formdata` calls the same number
`max_memory` when it is set on the handler instead of in the global block.

Both directories are created and write-probed when the config loads, so a typo or
a read-only mount fails `caddy validate` and reload rather than returning `500`
on every upload.

These options are Caddyfile-only. A JSON config has no global block, so set the
per-module fields instead (`root_dir`, `temp_dir`, `max_size`, `max_parts`,
`verify_mime`) on each handler that needs them.

## `savefile`

```
savefile <mimetype_filter> <destination_template>
```

`<mimetype_filter>` is an exact type (`application/pdf`), a wildcard subtype
(`image/*`), or the catch-all `*/*`. Write `*/*` rather than a bare `*`: Caddy
scans the first argument of a handler directive for a matcher token, so a leading
`*` is consumed as a catch-all request matcher, leaving the rule one argument
short and failing adaptation with `wrong argument count`. In a JSON config the
same filter is `"mime_filter": "*/*"`, and nothing is scanned.

A part that matches no rule is drained and discarded. Several `savefile` lines may
appear in one block; each part is claimed by at most one of them, in the order the
rules were written.

Because that matcher scan exists, a rule may carry a matcher of its own. Both of
these store only requests under `/upload`:

```
savefile /upload image/* images/{uuid}_{filename}

@uploads path /upload*
savefile @uploads image/* images/{uuid}_{filename}
```

Only `POST`, `PUT` and `PATCH` are handled. A `GET` or `HEAD` that reaches a
route holding rules is passed through untouched.

`<destination_template>` is relative to `root_dir`, or absolute as long as it
resolves inside `root_dir`. It supports:

- `{uuid}`: a fresh version 4 UUID per file part, so two parts sharing a filename
  cannot overwrite each other
- `{filename}`: the client filename, sanitized
- any Caddy placeholder, for example `{http.request.host}`

Caddy's Caddyfile adapter rewrites the `{uuid}` shorthand into
`{http.request.uuid}` before the directive sees it. The handler claims that form
back, so `{uuid}` always means a per-part UUID and not a per-request one.

The equivalent JSON, for a JSON config:

```json
{
	"handler": "savefile",
	"mime_filter": "image/*",
	"destination": "images/{uuid}_{filename}",
	"root_dir": "/srv/www/uploads",
	"max_size": 10485760
}
```

Handlers accept `mime_filter`, `destination`, `root_dir`, `temp_dir`, `max_size`,
`max_parts` and `verify_mime`.

## `formdata`

`formdata` takes no arguments. It parses the request body so templates can read
the submitted fields, and it is also the directive that lets a route accept form
submissions without any upload rule. Only `POST`, `PUT` and `PATCH` are handled.

Bodies it understands:

- `application/x-www-form-urlencoded`
- `multipart/form-data`
- `application/json` and any `*/*+json` subtype

Fields reach templates two ways:

```html
{{.Req.Form.Get "username"}}
{{placeholder "http.vars.form.username"}}
```

`.Req.Form` is the standard library's `url.Values`, populated by the directive,
with body values first and the URL query merged after. JSON is flattened, because
`url.Values` is flat:

| JSON | Result |
| --- | --- |
| `{"address": {"city": "x"}}` | `address.city` |
| `{"tags": ["a", "b"]}` | `Form["tags"]` holds both; `{http.vars.form.tags}` is `a` |
| `{"items": [{"name": "n"}]}` | `items.0.name` |
| `{"count": 1000000000}` | `1000000000`, literal text, no float rounding |
| `{"missing": null}` | an empty value |

Handlers accept `max_memory`, `max_parts`, `temp_dir` and `verify_mime`.
`max_memory` is the per-field and whole-JSON-body ceiling, and it is the same
number the global block calls `max_size`. In a Caddyfile, `formdata` takes no
arguments and no block, so a Caddyfile route always uses the global values; the
per-handler fields exist for a JSON config.

Because JSON counts as form data, a route containing `formdata` folds a JSON body
into fields rather than storing it. Leave `formdata` out of a route if you want a
JSON payload saved as a file.

## `{{ savefile filename content }}`

Enable the action under the templates handler:

```caddyfile
templates {
	extensions {
		savefile
	}
}
```

Then, from any template:

```html
{{$receipt := savefile (printf "receipts/%s.txt" $user) $notes}}
<p>Wrote {{$receipt}}</p>
```

- `filename` is a path inside `root_dir`. Every segment is sanitized and the
  resolved path is confined to `root_dir`.
- `content` may be a `string`, `[]byte`, or any `io.Reader`, and is written
  verbatim with no encoding changes. String-kinded template types such as
  `template.HTML` work too.
- The action returns the path relative to `root_dir`. Escaping `root_dir` or
  exceeding `max_size` fails template execution.

The extension accepts `root_dir`, `max_size` and `temp_dir`. In a JSON config
they go under the field Caddy names `match`, which is the key the templates
handler uses for its extension map:

```json
{
	"handler": "templates",
	"match": {
		"savefile": {
			"root_dir": "/srv/www/uploads",
			"max_size": 10485760,
			"temp_dir": "/tmp/caddy-uploads"
		}
	}
}
```

## Examples

Which directive a route needs:

| Goal | Directives |
| --- | --- |
| Read fields and store nothing | `formdata` |
| Read fields and store uploads | `formdata` plus `savefile` rules |
| Store uploads, ignore fields | `savefile` rules |
| Store a JSON payload as a file | `savefile application/json ...`, no `formdata` |
| Write a file from a template | the `savefile` templates extension |

Every complete configuration below was adapted with `caddy adapt` and loaded with
`caddy validate` against Caddy v2.11.9, so each one parses and provisions. The two
JSON fragments are checked as handlers inside a route, because they are
fragments. Examples without a global block take the defaults, so `root_dir` is
`./uploads`.

### Fields only, no uploads

A contact form needs `formdata` and nothing else. With no `savefile` rule,
nothing is ever written to disk.

```caddyfile
{
	savefile {
		root_dir /srv/www/uploads
		temp_dir /tmp
	}
}

example.com {
	root * /srv/www

	handle /contact* {
		formdata
		respond "{{include \"/thanks.html\"}}" 200
		templates
	}

	file_server
}
```

`thanks.html` reads the fields either way:

```html
<p>Thanks {{.Req.Form.Get "name"}},
we will reply to {{placeholder "http.vars.form.email"}}.</p>
```

```sh
curl -sS https://example.com/contact \
  -d name=Ada -d email=ada@example.com -d message=hello
```

### A JSON body folded into fields

The same directive accepts JSON. Note the content type: `curl -d` sends
`application/x-www-form-urlencoded` unless it is told otherwise.

```caddyfile
example.com {
	root * /srv/www

	handle /api/signup {
		formdata
		respond "{{include \"/signup.html\"}}" 200
		templates
	}
}
```

```sh
curl -sS https://example.com/api/signup \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada","address":{"city":"London"},"tags":["a","b"]}'
```

`signup.html` reaches the nested value as
`{{.Req.Form.Get "address.city"}}` and the array as
`{{.Req.Form.Get "tags"}}`, which is `a`. Nested keys are dotted paths, so
nothing has to walk a tree.

### A JSON body stored verbatim

Leave `formdata` out and the same bytes become one file part routed by type, so
the payload lands on disk unchanged.

```caddyfile
example.com {
	handle /events* {
		savefile application/json events/{uuid}.json
		respond 204
	}
}
```

```sh
curl -sS -o /dev/null -w '%{http_code}\n' https://example.com/events/ingest \
  -H 'Content-Type: application/json' -d '{"event":"signup"}'
```

That writes `events/<uuid>.json` byte for byte and answers `204`. The route is
not a form route at all, so a body that claims to be a form is stored as one
blob too.

### One file, no multipart

A route with a rule accepts a plain body, so `curl -T` works without a form:

```caddyfile
example.com {
	handle /upload/raw {
		savefile image/* images/{uuid}_{filename}
		respond 201
	}
}
```

```sh
curl -sS -o /dev/null -w '%{http_code}\n' \
  -T photo.png -H 'Content-Type: image/png' https://example.com/upload/raw
```

The body carries no filename, so it is named after the request path:
`{filename}` is `raw`, extended to `raw.png` from the detected type. The rule's
own `{uuid}` keeps two concurrent uploads to the same path apart.

### Fields, a file, and a template that writes a receipt

```caddyfile
example.com {
	root * /srv/www

	handle /order {
		formdata
		savefile application/pdf invoices/{uuid}.pdf

		respond "{{include \"/receipt.html\"}}" 200

		templates {
			extensions {
				savefile
			}
		}
	}

	file_server
}
```

`receipt.html` uses the uploaded fields and then writes a file of its own:

```html
{{$wrote := savefile (printf "receipts/%s.txt" (.Req.Form.Get "order_id"))
  (printf "total=%s\n" (.Req.Form.Get "total"))}}
<p>Order recorded, wrote {{$wrote}}.</p>
```

```sh
curl -sS https://example.com/order \
  -F order_id=A-1024 -F total=42.00 -F invoice=@invoice.pdf
```

That stores `invoices/<uuid>.pdf` through the directive and
`receipts/A-1024.txt` through the action, and the page reports the relative path
it wrote.

### Strict MIME verification, site-wide

`verify_mime` is a global option, so one line changes the policy for every route
on the site. There is no per-route form of it.

```caddyfile
{
	savefile {
		root_dir /srv/www/uploads
		verify_mime strict
	}
}

example.com {
	handle /upload* {
		formdata
		savefile */* files/{uuid}_{filename}
		respond 200
	}
}
```

```sh
# declaration and content agree, so the part is stored
curl -sS -o /dev/null -w '%{http_code}\n' https://example.com/upload \
  -H 'Content-Type: image/png' --data-binary @photo.png

# declaration disagrees with the content, so the request is refused
curl -sS -o /dev/null -w '%{http_code}\n' https://example.com/upload \
  -H 'Content-Type: image/png' --data-binary @page.html
```

The first request answers `200` and writes the file, the second answers `415`
and writes nothing.

### Everything at once

Every option of the global block, four rules, the action and the file server:

```caddyfile
{
	debug

	savefile {
		root_dir /srv/www/uploads
		max_size 25MB
		max_parts 50
		temp_dir /tmp/caddy-uploads
		verify_mime on
	}
}

example.com {
	root * /srv/www

	handle /upload* {
		formdata

		savefile image/* images/{uuid}_{filename}
		savefile application/pdf docs/{uuid}_{filename}
		savefile text/plain notes/{uuid}_{filename}
		savefile */* other/{uuid}_{filename}

		respond "{{include \"/process.html\"}}" 200

		templates {
			extensions {
				savefile
			}
		}
	}

	file_server
}
```

Rules are tried in order, so the catch-all `*/*` at the end claims whatever the
three specific rules did not. Without it, an unmatched part is discarded.

### The same site as JSON

A JSON config has no global block, so each handler carries its own values, and
sizes are plain byte counts (26214400 is 25MB).

```json
{
	"admin": {
		"disabled": true
	},
	"apps": {
		"http": {
			"servers": {
				"srv0": {
					"listen": [":8080"],
					"routes": [
						{
							"match": [{"host": ["example.com"]}],
							"handle": [
								{
									"handler": "subroute",
									"routes": [
										{
											"match": [{"path": ["/upload*"]}],
											"handle": [
												{
													"handler": "formdata",
													"max_memory": 26214400,
													"max_parts": 50,
													"temp_dir": "/tmp/caddy-uploads",
													"verify_mime": "on"
												},
												{
													"handler": "savefile",
													"mime_filter": "image/*",
													"destination": "images/{uuid}_{filename}",
													"root_dir": "/srv/www/uploads",
													"temp_dir": "/tmp/caddy-uploads",
													"max_size": 26214400,
													"max_parts": 50,
													"verify_mime": "on"
												},
												{
													"handler": "savefile",
													"mime_filter": "application/pdf",
													"destination": "docs/{uuid}_{filename}",
													"root_dir": "/srv/www/uploads",
													"max_size": 26214400
												},
												{
													"handler": "templates",
													"mime_types": ["text/html"],
													"match": {
														"savefile": {
															"root_dir": "/srv/www/uploads",
															"max_size": 26214400,
															"temp_dir": "/tmp/caddy-uploads"
														}
													}
												}
											]
										},
										{
											"handle": [
												{
													"handler": "file_server",
													"root": "/srv/www"
												}
											]
										}
									]
								}
							],
							"terminal": true
						}
					]
				}
			}
		}
	}
}
```

## Body handling

One body, read once. Which directive claims it depends on the media type:

| Content-Type | Claimed by | Result |
| --- | --- | --- |
| `multipart/form-data` | `formdata` or `savefile` | fields into `Values`, file parts buffered in `temp_dir` |
| `application/x-www-form-urlencoded` | `formdata` | `Values` |
| `application/json`, `*/*+json` | `formdata` | folded into `Values` |
| anything else | `savefile` | one file part, so a plain POST of a single file is routed by MIME type |

That last row means `curl --data-binary @photo.png -H 'Content-Type: image/png'`
works against a route with an `image/*` rule. The part is named from
`Content-Disposition` if the client sent one, otherwise the last path segment of
the request path, otherwise `upload`, with an extension derived from the resolved
type when the name has none. A `POST` to `/upload/raw` therefore arrives as
`raw`, and `{filename}` resolves to `raw.png` for an `image/png` body.

Both directives ignore every method except `POST`, `PUT` and `PATCH`, so the same
route answering a `GET` does no body work at all.

That extension comes from a pinned table, so a missing name does not inherit a
stdlib oddity such as `.ehtml` for `text/html`:

| Type | Extension |
| --- | --- |
| `application/gzip` | `.gz` |
| `application/json` | `.json` |
| `application/pdf` | `.pdf` |
| `application/zip` | `.zip` |
| `image/gif` | `.gif` |
| `image/jpeg` | `.jpg` |
| `image/png` | `.png` |
| `image/svg+xml` | `.svg` |
| `image/webp` | `.webp` |
| `text/csv` | `.csv` |
| `text/html` | `.html` |
| `text/plain` | `.txt` |
| `video/mp4` | `.mp4` |

Any other type falls back to the first extension the standard library reports for
it.

## MIME verification

A client's `Content-Type` is a claim, so every part is sniffed with
`http.DetectContentType` and both types are recorded: `mime_type` is what the
bytes are, `declared_mime_type` is what the client said.

Routing is decided as follows:

- The **declaration decides**, because sniffing is a magic-number heuristic. A
  real `.xlsx` sniffs as `application/zip`, so routing purely by content would
  send spreadsheets to the wrong rule.
- A missing or `application/octet-stream` declaration defers to the content.
- **Active content never passes under a disagreeing declaration.** `text/html`,
  `text/xml`, `application/xml`, `image/svg+xml`, `application/javascript`,
  `text/javascript` and anything `+xml` must match a rule for the detected type.
  A page claiming `image/png` therefore does not match `image/*` and is discarded.

`verify_mime` narrows or widens that policy:

| Mode | Behaviour |
| --- | --- |
| `on` (default) | the rules above |
| `strict` | the declaration must equal the detected type, otherwise `415` |
| `off` | the declaration is trusted outright |

## Limits, errors and cleanup

| Situation | Result |
| --- | --- |
| part larger than `max_size` | `413`, partial file removed |
| more than `max_parts` parts | `413` |
| declared `Content-Length` over the ceiling | `413` before reading |
| destination resolving outside `root_dir` | `400` |
| malformed body | `400` |
| `verify_mime strict` mismatch | `415` |
| unusable `root_dir` or `temp_dir` | config load fails |
| route reached with a method other than `POST`, `PUT`, `PATCH` | nothing is parsed or written |

Multipart file parts are buffered into `temp_dir` and moved to their destination,
so a part is either fully written or not written at all. Temp files that no rule
claimed are removed when the request finishes, not when the client disconnects,
so an upload is never lost to a client that closes the connection early.

## Logging

With `debug` enabled in the global block, each save and each discarded part is
logged with its filename, field, size and both MIME types. When a declaration
loses to the content, that is logged at warn level, because it usually means a
client is lying or a rule is misconfigured.

## Placeholders

| Placeholder | Description |
| --- | --- |
| `{http.vars.form.<key>}` | first submitted value for form field `<key>` |

Saved-file metadata is deliberately not published to the vars tree. The template
action returns the path it wrote, and `SavedFileInfo` (with `field_name`,
`original_filename`, `saved_path`, `filesize`, `mime_type`, `declared_mime_type`)
is what the handler records internally.

## Build and test

```sh
timeout 600 ./build.sh         # scratch image, tagged caddy-savefile:test
timeout 180 tests/e2e/run.sh   # starts that image and checks it over HTTP
```

`tests/e2e/run.sh` mounts `tests/e2e/config` read-only, so it uses its own
Caddyfile and pages rather than the demo config in `www/`. It drives the
container from the inside with `nc` and toybox `wget` and asserts what lands on
disk under `root_dir`.

There is a second, in-process layer: `tests/e2e/e2e_test.go` loads a real config
on a free port and uploads through it. The whole suite, from the builder image:

```sh
timeout 280 docker run --rm -v caddy-sf-mod:/mod -v "$PWD:/src" -w /src \
  -e GOMODCACHE=/mod -e GOCACHE=/mod/gocache caddy:2.11.9-builder-alpine \
  sh -c 'gofmt -w savefile.go pkg tests && go vet ./... && go test ./... -count=1 -timeout 120s'
```

Go tooling runs in that builder image only, never on the host. The sanitizer is
fuzz-tested:

```sh
timeout 280 docker run --rm -v caddy-sf-mod:/mod -v "$PWD:/src" -w /src \
  -e GOMODCACHE=/mod -e GOCACHE=/mod/gocache caddy:2.11.9-builder-alpine \
  sh -c 'cd pkg/paths && go test -run Fuzz -fuzz FuzzSanitizeFilename -fuzztime 30s'
```

## Layout

```
savefile.go     the only root file: blank imports so modules register
pkg/config      global options, defaults, size parsing, shared store
pkg/paths       sanitization, root_dir containment, UUIDs, MIME policy, file moves
pkg/formbody    one-read form state shared by both directives
pkg/formdata    the formdata directive
pkg/savefile    the savefile directive
pkg/template    the templates action
tests/e2e       in-process config test and the in-image harness
www             example Caddyfile, form page, result page
```

## Compatibility

Built against Caddy `v2.11.9`; `go.mod` requires Go 1.26. The image is a scratch
container built from `caddy:2.11.9-builder-alpine`, with toybox providing the
shell, so no libc or shell is inherited from a base image.

`AGENTS.md` holds working notes for agents (kept out of git by `.gitignore`),
`spec.md` is the technical specification and is kept in step with this document
and the code, and `NOTICE` records licensing.
