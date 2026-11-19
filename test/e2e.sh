#!/bin/sh
# e2e test runner — boots the image ./build.sh produces and exercises the
# formdata directive, the savefile rules, the templates action, the three MIME
# verification modes, limits, and startup validation.
#
#   sh test/e2e.sh
#
# E2E_IMAGE overrides the image (default: the NOTICE slug with a :test tag).
# The script only runs Docker and host-side HTTP checks; it never builds.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

TMP=$(mktemp -d /tmp/caddy-savefile-e2e-XXXXXX)
PORT=9099
STRICT_PORT=9100
OFF_PORT=9101
SLUG=$(sed -n '1s/^[^:]*: //p' ./NOTICE)
IMAGE="${E2E_IMAGE:-${SLUG}:test}"
CONTAINER_NAME="caddysavefile-e2e-runner"
STRICT_NAME="caddysavefile-e2e-strict"
OFF_NAME="caddysavefile-e2e-off"
PASS=0
FAIL=0

cleanup() {
	for name in "$CONTAINER_NAME" "$STRICT_NAME" "$OFF_NAME"; do
		timeout 20 docker exec "$name" /bin/sh -c 'rm -rf /data/*' >/dev/null 2>&1 || true
		timeout 20 docker rm -f "$name" >/dev/null 2>&1 || true
	done
	rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

pass() {
	PASS=$((PASS + 1))
	printf '  PASS: %s\n' "$1"
}

fail() {
	FAIL=$((FAIL + 1))
	printf '  FAIL: %s\n' "$1"
	if [ "$#" -gt 1 ]; then
		printf '    %s\n' "$2"
	fi
}

check_eq() {
	if [ "$2" = "$3" ]; then
		pass "$1"
	else
		fail "$1" "expected: $2; got: $3"
	fi
}

check_has() {
	if printf '%s' "$3" | grep -qF -- "$2"; then
		pass "$1"
	else
		fail "$1" "missing: $2; got: $3"
	fi
}

check_absent() {
	if printf '%s' "$3" | grep -qF -- "$2"; then
		fail "$1" "unexpected: $2; got: $3"
	else
		pass "$1"
	fi
}

request() {
	REQ_PORT=$1
	REQ_METHOD=$2
	REQ_PATH=$3
	shift 3
	STATUS=$(timeout 15 curl -sS --max-time 10 -X "$REQ_METHOD" \
		-D "$TMP/headers" -o "$TMP/body" -w '%{http_code}' "$@" \
		"http://127.0.0.1:$REQ_PORT$REQ_PATH" 2>/dev/null) || STATUS=000
}

body_has() { check_has "$1" "$2" "$(cat "$TMP/body" 2>/dev/null || true)"; }
body_absent() { check_absent "$1" "$2" "$(cat "$TMP/body" 2>/dev/null || true)"; }

read_file() {
	timeout 20 docker exec "$CONTAINER_NAME" /bin/cat "$1" 2>/dev/null || true
}

count_files() {
	timeout 10 find "$1" -type f 2>/dev/null | wc -l | tr -d ' '
}

count_entries() {
	ls -1 "$1" 2>/dev/null | wc -l | tr -d ' '
}

assert_count() {
	ASSERT_DESC=$1
	ASSERT_DIR=$2
	ASSERT_WANT=$3
	ASSERT_GOT=$(count_files "$ASSERT_DIR")
	check_eq "$ASSERT_DESC" "$ASSERT_WANT" "$ASSERT_GOT"
}

assert_entry_count() {
	ASSERT_DESC=$1
	ASSERT_DIR=$2
	ASSERT_WANT=$3
	ASSERT_GOT=$(count_entries "$ASSERT_DIR")
	check_eq "$ASSERT_DESC" "$ASSERT_WANT" "$ASSERT_GOT"
}

assert_name() {
	ASSERT_DESC=$1
	ASSERT_DIR=$2
	ASSERT_PATTERN=$3
	if ls -1 "$ASSERT_DIR" 2>/dev/null | grep -Eq -- "$ASSERT_PATTERN"; then
		pass "$ASSERT_DESC"
	else
		fail "$ASSERT_DESC" "no name matches $ASSERT_PATTERN in $(ls -1 "$ASSERT_DIR" 2>/dev/null | tr '\n' ' ')"
	fi
}

wait_ready() {
	WAIT_PORT=$1
	WAIT_I=0
	while [ "$WAIT_I" -lt 40 ]; do
		if timeout 5 curl -sf --max-time 2 "http://127.0.0.1:$WAIT_PORT/health" >/dev/null 2>&1; then
			return 0
		fi
		WAIT_I=$((WAIT_I + 1))
		sleep 0.25
	done
	return 1
}

start_container() {
	START_NAME=$1
	START_CONFIG=$2
	START_PORT=$3
	timeout 60 docker run -d --no-healthcheck --name "$START_NAME" \
		-v "$START_CONFIG:/etc/caddy/Caddyfile:ro" \
		-v "$TMP:/data" \
		-p "127.0.0.1:$START_PORT:80" \
		"$IMAGE" run --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
}

fail_start() {
	echo "container $1 did not become ready" >&2
	timeout 20 docker logs "$1" 2>&1 | tail -n 40 || true
	exit 1
}

validate_ok() {
	VALIDATE_DESC=$1
	VALIDATE_CFG=$2
	VALIDATE_STATUS=0
	VALIDATE_OUT=$(timeout 30 docker run --rm \
		-v "$VALIDATE_CFG:/etc/caddy/Caddyfile:ro" \
		-v "$TMP:/data" \
		"$IMAGE" validate --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1) || VALIDATE_STATUS=$?
	if [ "$VALIDATE_STATUS" -eq 0 ]; then
		pass "$VALIDATE_DESC"
	else
		fail "$VALIDATE_DESC" "exit=$VALIDATE_STATUS output=$VALIDATE_OUT"
	fi
}

validate_fail() {
	VALIDATE_DESC=$1
	VALIDATE_CFG=$2
	VALIDATE_NEEDLE=$3
	VALIDATE_STATUS=0
	VALIDATE_OUT=$(timeout 30 docker run --rm \
		-v "$VALIDATE_CFG:/etc/caddy/Caddyfile:ro" \
		-v "$TMP:/data" \
		"$IMAGE" validate --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1) || VALIDATE_STATUS=$?
	if [ "$VALIDATE_STATUS" -ne 0 ] && printf '%s' "$VALIDATE_OUT" | grep -qF -- "$VALIDATE_NEEDLE"; then
		pass "$VALIDATE_DESC"
	else
		fail "$VALIDATE_DESC" "expected non-zero exit containing: $VALIDATE_NEEDLE; exit=$VALIDATE_STATUS output=$VALIDATE_OUT"
	fi
}

timeout 20 docker image inspect "$IMAGE" >/dev/null 2>&1 || {
	echo "test/e2e.sh: image $IMAGE not found; build it with ./build.sh" >&2
	exit 1
}

# ── Fixtures ──────────────────────────────────────────────────────────────
mkdir -p "$TMP/www" "$TMP/tpl"

cp www/index.html www/process.html "$TMP/www/"

cat > "$TMP/www/contact.html" << 'HTML'
user={{.Req.Form.Get "username"}}
city={{.Req.Form.Get "address.city"}}
cityPlaceholder={{placeholder "http.vars.form.address.city"}}
tags={{.Req.Form.Get "tags"}}
HTML

cat > "$TMP/tpl/ok.html" << 'HTML'
{{$p := savefile "writes/ok.txt" "ok-content"}}written={{$p}}
HTML

cat > "$TMP/tpl/traverse.html" << 'HTML'
{{$p := savefile "../../evil.txt" "evil-content"}}written={{$p}}
HTML

cat > "$TMP/tpl/escape.html" << 'HTML'
{{$p := savefile "/tmp/escape.txt" "escape-content"}}written={{$p}}
HTML

printf '\211PNG\r\n\032\n fake png\n' > "$TMP/put.png"
printf '%%PDF-1.4 fake\n' > "$TMP/put.pdf"
printf 'plain notes\n' > "$TMP/put.txt"
printf 'ignored video bytes\n' > "$TMP/put.mp4"
printf '<!DOCTYPE html><html><body>not an image</body></html>' > "$TMP/page.html"
head -c 1500000 /dev/zero | tr '\0' 'a' > "$TMP/big"
printf 'not a directory\n' > "$TMP/notadir"

cat > "$TMP/Caddyfile" << 'CADDY'
{
	admin off
	debug

	savefile {
		root_dir   /data/uploads
		max_size   1MB
		max_parts  6
		temp_dir   /data/tmp
		verify_mime on
	}
}

:80 {
	root * /data/www

	# Fields, uploads, the receipt template and the savefile action.
	handle /upload* {
		formdata

		savefile image/* images/{uuid}_{filename}
		savefile application/pdf docs/{uuid}_{filename}
		savefile text/plain notes/{uuid}_{filename}

		respond "{{include \"/process.html\"}}" 200

		templates {
			extensions {
				savefile
			}
		}
	}

	# One raw body, routed by its MIME type.
	handle /raw* {
		savefile image/* raw/{uuid}_{filename}
		respond "stored" 201
	}

	# A JSON payload stored verbatim because formdata is absent.
	handle /events* {
		savefile application/json events/{uuid}.json
		respond "stored" 204
	}

	# The catch-all filter.
	handle /any* {
		savefile */* other/{uuid}_{filename}
		respond "stored" 200
	}

	# Fields only: formdata parses, nothing is written.
	handle /contact* {
		formdata
		respond "{{include \"/contact.html\"}}" 200
		templates
	}

	# A destination that resolves outside root_dir.
	handle /escape* {
		savefile image/* ../outside/{uuid}_{filename}
		respond "stored" 200
	}

	# The {{ savefile }} action over files served by the file server.
	handle_path /tpl/* {
		templates {
			extensions {
				savefile
			}
		}
		file_server {
			root /data/tpl
		}
	}

	handle /health {
		respond "alive" 200
	}

	file_server
}
CADDY

cat > "$TMP/strict.Caddyfile" << 'CADDY'
{
	admin off
	debug

	savefile {
		root_dir   /data/strict
		max_size   1MB
		temp_dir   /data/tmp-strict
		verify_mime strict
	}
}

:80 {
	route /health {
		respond "alive"
	}
	route /upload {
		savefile image/* images/{uuid}_{filename}
		respond "stored" 200
	}
}
CADDY

cat > "$TMP/off.Caddyfile" << 'CADDY'
{
	admin off
	debug

	savefile {
		root_dir   /data/off
		max_size   1MB
		temp_dir   /data/tmp-off
		verify_mime off
	}
}

:80 {
	route /health {
		respond "alive"
	}
	route /upload {
		savefile image/* images/{uuid}_{filename}
		respond "stored" 200
	}
}
CADDY

# ── Test 0: the image runs the versions go.mod pins ──────────────────────
echo ""
echo "=== Test 0: image contents ==="

if ! VERSIONS=$(timeout 30 docker run --rm --entrypoint /bin/cat "$IMAGE" /usr/share/licenses/versions.txt 2>&1); then
	fail "read image versions" "$VERSIONS"
else
	WANT=$(awk '$1 == "github.com/caddyserver/caddy/v2" && $2 ~ /^v/ {print $2}' go.mod | head -n1)
	GOT=$(printf '%s\n' "$VERSIONS" | awk '$1 == "dep" && $2 == "github.com/caddyserver/caddy/v2" {print $3}')
	check_eq "caddy version matches go.mod" "$WANT" "$GOT"
	if printf '%s\n' "$VERSIONS" | grep -q 'github.com/frinknet/caddy-savefile'; then
		pass "plugin is linked into the image"
	else
		fail "plugin is linked into the image"
	fi
fi

if ! MODULES=$(timeout 30 docker run --rm --entrypoint /bin/caddy "$IMAGE" list-modules 2>&1); then
	fail "caddy list-modules" "$MODULES"
else
	for module in http.handlers.formdata http.handlers.savefile http.handlers.templates.functions.savefile; do
		if printf '%s\n' "$MODULES" | grep -qx "$module"; then
			pass "module $module is registered"
		else
			fail "module $module is registered"
		fi
	done
fi

# ── Start Caddy ───────────────────────────────────────────────────────────
echo ""
echo "=== Starting Caddy container ==="
start_container "$CONTAINER_NAME" "$TMP/Caddyfile" "$PORT"
if ! wait_ready "$PORT"; then
	fail_start "$CONTAINER_NAME"
fi
echo "Caddy ready"

# ── Test 1: provisioning prepared the directories ─────────────────────────
echo ""
echo "=== Test 1: provisioned directories ==="
if [ -d "$TMP/uploads" ]; then
	pass "root_dir was created at config load"
else
	fail "root_dir was created at config load"
fi
if [ -d "$TMP/tmp" ]; then
	pass "temp_dir was created at config load"
else
	fail "temp_dir was created at config load"
fi
assert_count "root_dir starts empty" "$TMP/uploads" 0

# ── Test 2: static file ───────────────────────────────────────────────────
echo ""
echo "=== Test 2: static file ==="
request "$PORT" GET /
check_eq "index status" "200" "$STATUS"
body_has "index page is served" "Upload a submission"

# ── Test 3: GET does no body work ─────────────────────────────────────────
echo ""
echo "=== Test 3: GET passes through untouched ==="
request "$PORT" GET /upload
check_eq "GET /upload status" "200" "$STATUS"
body_has "the process page renders" "Submission received from"
assert_count "GET wrote nothing" "$TMP/uploads" 0

# ── Test 4: urlencoded form and the receipt action ────────────────────────
echo ""
echo "=== Test 4: urlencoded form ==="
request "$PORT" POST /upload -d 'username=alice&notes=hello+there'
check_eq "urlencoded status" "200" "$STATUS"
body_has "the form field reaches the template" "Submission received from alice"
body_has "the action reports its path" "receipts/alice-notes.txt"
check_eq "the receipt holds the field value" "hello there" "$(read_file /data/uploads/receipts/alice-notes.txt)"

# ── Test 5: multipart uploads and MIME routing ────────────────────────────
echo ""
echo "=== Test 5: multipart uploads ==="
request "$PORT" POST /upload \
	-F username=bob -F notes=note \
	-F "photo=@$TMP/put.png;type=image/png" \
	-F "doc=@$TMP/put.pdf;type=application/pdf" \
	-F "clip=@$TMP/put.mp4;type=video/mp4" \
	-F "extra=@$TMP/put.txt;type=text/plain"
check_eq "multipart status" "200" "$STATUS"
body_has "the multipart field reaches the template" "Submission received from bob"
assert_entry_count "image/* rule stored one file" "$TMP/uploads/images" 1
assert_entry_count "application/pdf rule stored one file" "$TMP/uploads/docs" 1
assert_entry_count "text/plain rule stored one file" "$TMP/uploads/notes" 1
assert_name "image is named <uuid>_put.png" "$TMP/uploads/images" '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}_put\.png$'
assert_name "document is named <uuid>_put.pdf" "$TMP/uploads/docs" '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}_put\.pdf$'
assert_name "text part is named <uuid>_put.txt" "$TMP/uploads/notes" '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}_put\.txt$'
check_eq "the multipart receipt holds the field value" "note" "$(read_file /data/uploads/receipts/bob-notes.txt)"
if [ -e "$TMP/uploads/videos" ]; then
	fail "an unmatched video part was discarded" "videos/ exists"
else
	pass "an unmatched video part was discarded"
fi

request "$PORT" POST /upload \
	-F "one=@$TMP/put.png;filename=dup.png;type=image/png" \
	-F "two=@$TMP/put.png;filename=dup.png;type=image/png"
check_eq "duplicate filenames status" "200" "$STATUS"
assert_entry_count "duplicate filenames both stored" "$TMP/uploads/images" 3

request "$PORT" POST /upload -F "photo=@$TMP/put.png;filename=../../evil.png;type=image/png"
check_eq "traversal filename status" "200" "$STATUS"
assert_name "traversal segments were stripped" "$TMP/uploads/images" '_evil\.png$'
check_eq "no file escaped below root_dir" "0" "$(timeout 10 find "$TMP" -name 'evil.png' 2>/dev/null | wc -l | tr -d ' ')"

request "$PORT" POST /upload -F "photo=@$TMP/page.html;filename=page.png;type=image/png"
check_eq "active content status" "200" "$STATUS"
assert_entry_count "active content was discarded, not stored" "$TMP/uploads/images" 4

# ── Test 6: a raw body is routed by MIME type ─────────────────────────────
echo ""
echo "=== Test 6: raw bodies ==="
request "$PORT" POST /raw --data-binary "@$TMP/put.png" -H 'Content-Type: image/png'
check_eq "raw POST status" "201" "$STATUS"
assert_entry_count "raw body stored" "$TMP/uploads/raw" 1
assert_name "raw body is named from the request path" "$TMP/uploads/raw" '_raw\.png$'
body_absent "the response is not the uploaded bytes" "fake png"

request "$PORT" PUT /raw --data-binary "@$TMP/put.png" -H 'Content-Type: image/png'
check_eq "raw PUT status" "201" "$STATUS"
assert_entry_count "PUT is handled like POST" "$TMP/uploads/raw" 2

request "$PORT" GET /raw
assert_entry_count "GET writes nothing" "$TMP/uploads/raw" 2

# ── Test 7: JSON is stored verbatim without formdata ──────────────────────
echo ""
echo "=== Test 7: JSON stored verbatim ==="
request "$PORT" POST /events -H 'Content-Type: application/json' --data-binary '{"event":"signup"}'
check_eq "JSON status" "204" "$STATUS"
assert_entry_count "JSON body stored as one file" "$TMP/uploads/events" 1
assert_name "JSON file is named <uuid>.json" "$TMP/uploads/events" '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.json$'
JSON_NAME=$(ls -1 "$TMP/uploads/events" | head -n1)
check_eq "JSON bytes were not rewritten" '{"event":"signup"}' "$(read_file "/data/uploads/events/$JSON_NAME")"

# ── Test 8: the catch-all filter ──────────────────────────────────────────
echo ""
echo "=== Test 8: catch-all filter ==="
request "$PORT" POST /any --data-binary "@$TMP/put.mp4" -H 'Content-Type: video/mp4'
check_eq "catch-all status" "200" "$STATUS"
assert_entry_count "catch-all stored the part" "$TMP/uploads/other" 1
assert_name "catch-all keeps the derived extension" "$TMP/uploads/other" '_any\.mp4$'

# ── Test 9: fields only, no writes ────────────────────────────────────────
echo ""
echo "=== Test 9: fields only ==="
BEFORE=$(count_files "$TMP/uploads")
request "$PORT" POST /contact -d 'username=carol&address.city=London&tags=a'
check_eq "urlencoded fields status" "200" "$STATUS"
body_has "urlencoded field" "user=carol"
body_has "dotted JSON-style key" "city=London"
body_has "placeholder mirrors the field" "cityPlaceholder=London"
body_has "array value is flattened" "tags=a"

request "$PORT" POST /contact -H 'Content-Type: application/json' \
	--data-binary '{"username":"dave","address":{"city":"Paris"},"tags":["x","y"]}'
check_eq "JSON fields status" "200" "$STATUS"
body_has "JSON field" "user=dave"
body_has "nested JSON key becomes dotted" "city=Paris"
body_has "placeholder reaches nested JSON" "cityPlaceholder=Paris"
body_has "JSON array value is flattened" "tags=x"
check_eq "fields only wrote nothing" "$BEFORE" "$(count_files "$TMP/uploads")"

# ── Test 10: limits ───────────────────────────────────────────────────────
echo ""
echo "=== Test 10: limits ==="
request "$PORT" POST /upload \
	-F f1=1 -F f2=2 -F f3=3 -F f4=4 -F f5=5 -F f6=6 -F f7=7
check_eq "more than max_parts becomes 413" "413" "$STATUS"

IMAGES_BEFORE=$(count_entries "$TMP/uploads/images")
request "$PORT" POST /upload -F "photo=@$TMP/big;type=image/png"
check_eq "a part over max_size becomes 413" "413" "$STATUS"
assert_entry_count "the oversized part left no file" "$TMP/uploads/images" "$IMAGES_BEFORE"

request "$PORT" POST /upload -H 'Content-Length: 7000000' \
	-H 'Content-Type: multipart/form-data; boundary=x' --data-binary 'x'
check_eq "a declared Content-Length over the ceiling becomes 413" "413" "$STATUS"

request "$PORT" POST /upload -H 'Content-Type: multipart/form-data' --data 'x'
check_eq "a multipart body without a boundary becomes 400" "400" "$STATUS"

request "$PORT" POST /upload -F "field=@$TMP/big;type=text/plain"
check_eq "a field over max_size becomes 413" "413" "$STATUS"

request "$PORT" POST /raw --data-binary "@$TMP/big" -H 'Content-Type: image/png'
check_eq "a raw body over max_size becomes 413" "413" "$STATUS"

# ── Test 11: method passthrough and PUT forms ─────────────────────────────
echo ""
echo "=== Test 11: methods ==="
BEFORE=$(count_files "$TMP/uploads")
request "$PORT" DELETE /upload
check_eq "DELETE passes through" "200" "$STATUS"
check_eq "DELETE writes nothing" "$BEFORE" "$(count_files "$TMP/uploads")"

request "$PORT" PUT /upload -d 'username=putuser&notes=putnote'
check_eq "PUT form status" "200" "$STATUS"
check_eq "PUT form wrote a receipt" "putnote" "$(read_file /data/uploads/receipts/putuser-notes.txt)"

# ── Test 12: the template action ──────────────────────────────────────────
echo ""
echo "=== Test 12: template action ==="
request "$PORT" GET /tpl/ok.html
check_eq "action status" "200" "$STATUS"
body_has "action returns the relative path" "written=writes/ok.txt"
check_eq "action content is verbatim" "ok-content" "$(read_file /data/uploads/writes/ok.txt)"

request "$PORT" GET /tpl/traverse.html
check_eq "traversal action status" "200" "$STATUS"
body_has "traversal segments were stripped" "written=evil.txt"
check_eq "the action wrote inside root_dir" "evil-content" "$(read_file /data/uploads/evil.txt)"
check_eq "the action wrote nothing outside root_dir" "1" "$(timeout 10 find "$TMP" -name 'evil.txt' 2>/dev/null | wc -l | tr -d ' ')"

request "$PORT" GET /tpl/escape.html
check_eq "an absolute action path fails" "500" "$STATUS"
check_eq "no file escaped root_dir" "0" "$(timeout 10 find "$TMP" -name 'escape.txt' 2>/dev/null | wc -l | tr -d ' ')"

# ── Test 13: destinations cannot leave root_dir ───────────────────────────
echo ""
echo "=== Test 13: destination containment ==="
request "$PORT" POST /escape --data-binary "@$TMP/put.png" -H 'Content-Type: image/png'
check_eq "an escaping destination becomes 400" "400" "$STATUS"
if [ -e "$TMP/outside" ]; then
	fail "the escaping destination wrote nothing" "outside/ exists"
else
	pass "the escaping destination wrote nothing"
fi

# ── Test 14: verify_mime strict ───────────────────────────────────────────
echo ""
echo "=== Test 14: verify_mime strict ==="
start_container "$STRICT_NAME" "$TMP/strict.Caddyfile" "$STRICT_PORT"
if ! wait_ready "$STRICT_PORT"; then
	fail_start "$STRICT_NAME"
fi

request "$STRICT_PORT" POST /upload --data-binary "@$TMP/put.png" -H 'Content-Type: image/png'
check_eq "a matching declaration is stored" "200" "$STATUS"
assert_entry_count "strict stored the matching part" "$TMP/strict/images" 1

request "$STRICT_PORT" POST /upload --data-binary "@$TMP/page.html" -H 'Content-Type: image/png'
check_eq "a mismatched declaration becomes 415" "415" "$STATUS"
assert_entry_count "strict wrote nothing for the mismatch" "$TMP/strict/images" 1

# ── Test 15: verify_mime off ──────────────────────────────────────────────
echo ""
echo "=== Test 15: verify_mime off ==="
start_container "$OFF_NAME" "$TMP/off.Caddyfile" "$OFF_PORT"
if ! wait_ready "$OFF_PORT"; then
	fail_start "$OFF_NAME"
fi

request "$OFF_PORT" POST /upload --data-binary "@$TMP/page.html" -H 'Content-Type: image/png'
check_eq "off trusts the declaration" "200" "$STATUS"
assert_entry_count "off stored the declared part" "$TMP/off/images" 1

# ── Test 16: debug logs record every decision ─────────────────────────────
echo ""
echo "=== Test 16: logs ==="
LOGS=$(timeout 20 docker logs "$CONTAINER_NAME" 2>&1) || fail "read container logs" "docker logs failed"

for entry in \
	'"msg":"parsed form body"' \
	'"msg":"saved part"' \
	'"msg":"discarded part"' \
	'"msg":"wrote template output"' \
	'routing a part by its content'
do
	if printf '%s' "$LOGS" | grep -qF -- "$entry"; then
		pass "log records $entry"
	else
		fail "log records $entry"
	fi
done

STRICT_LOGS=$(timeout 20 docker logs "$STRICT_NAME" 2>&1) || true
if printf '%s' "$STRICT_LOGS" | grep -qF 'declared MIME type does not match the content'; then
	pass "strict logs the MIME mismatch"
else
	fail "strict logs the MIME mismatch"
fi

# ── Test 17: startup validation ───────────────────────────────────────────
echo ""
echo "=== Test 17: startup validation ==="
validate_ok "main Caddyfile validates" "$TMP/Caddyfile"
validate_ok "strict Caddyfile validates" "$TMP/strict.Caddyfile"
validate_ok "off Caddyfile validates" "$TMP/off.Caddyfile"

NEG="$TMP/neg"
mkdir -p "$NEG"

cat > "$NEG/unknown-option.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		root_dir /data/uploads
		nope 1
	}
}
:8080 {
	respond "ok"
}
CADDY

cat > "$NEG/size-unit.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		max_size 2XB
	}
}
:8080 {
	respond "ok"
}
CADDY

cat > "$NEG/size-zero.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		max_size 0
	}
}
:8080 {
	respond "ok"
}
CADDY

cat > "$NEG/parts.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		max_parts 0
	}
}
:8080 {
	respond "ok"
}
CADDY

cat > "$NEG/verify-mime.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		verify_mime maybe
	}
}
:8080 {
	respond "ok"
}
CADDY

cat > "$NEG/root-file.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		root_dir /data/notadir
		temp_dir /data/tmp
	}
}
:8080 {
	savefile image/* images/{filename}
}
CADDY

cat > "$NEG/temp-file.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		root_dir /data/uploads
		temp_dir /data/notadir
	}
}
:8080 {
	savefile image/* images/{filename}
}
CADDY

cat > "$NEG/bare-star.Caddyfile" << 'CADDY'
{
	admin off
}
:8080 {
	route /x {
		savefile * images/{filename}
	}
}
CADDY

cat > "$NEG/no-args.Caddyfile" << 'CADDY'
{
	admin off
}
:8080 {
	route /x {
		savefile
	}
}
CADDY

cat > "$NEG/one-arg.Caddyfile" << 'CADDY'
{
	admin off
}
:8080 {
	route /x {
		savefile image/*
	}
}
CADDY

cat > "$NEG/three-args.Caddyfile" << 'CADDY'
{
	admin off
}
:8080 {
	route /x {
		savefile image/* images/x extra
	}
}
CADDY

cat > "$NEG/formdata-arg.Caddyfile" << 'CADDY'
{
	admin off
}
:8080 {
	route /x {
		formdata yes
	}
}
CADDY

cat > "$NEG/extension-arg.Caddyfile" << 'CADDY'
{
	admin off
	savefile {
		root_dir /data/uploads
		temp_dir /data/tmp
	}
}
:8080 {
	templates {
		extensions {
			savefile yes
		}
	}
}
CADDY

validate_fail "unknown global option fails" "$NEG/unknown-option.Caddyfile" 'unrecognized savefile option'
validate_fail "an unknown size unit fails" "$NEG/size-unit.Caddyfile" 'unknown size unit'
validate_fail "a zero size fails" "$NEG/size-zero.Caddyfile" 'size must be positive'
validate_fail "a non-positive max_parts fails" "$NEG/parts.Caddyfile" 'max_parts must be a positive integer'
validate_fail "an unknown verify_mime fails" "$NEG/verify-mime.Caddyfile" 'verify_mime must be one of'
validate_fail "an unusable root_dir fails" "$NEG/root-file.Caddyfile" 'root_dir'
validate_fail "an unusable temp_dir fails" "$NEG/temp-file.Caddyfile" 'temp_dir'
validate_fail "a bare * filter fails" "$NEG/bare-star.Caddyfile" 'wrong argument count'
validate_fail "savefile without arguments fails" "$NEG/no-args.Caddyfile" 'wrong argument count'
validate_fail "savefile with one argument fails" "$NEG/one-arg.Caddyfile" 'wrong argument count'
validate_fail "savefile with three arguments fails" "$NEG/three-args.Caddyfile" 'wrong argument count'
validate_fail "formdata with an argument fails" "$NEG/formdata-arg.Caddyfile" 'wrong argument count'
validate_fail "the template extension with an argument fails" "$NEG/extension-arg.Caddyfile" 'wrong argument count'

# ── Summary ───────────────────────────────────────────────────────────────
echo ""
echo "=========================================="
printf '  PASS: %s   FAIL: %s\n' "$PASS" "$FAIL"
echo "=========================================="
if [ "$FAIL" -eq 0 ]; then
	echo "ALL TESTS PASSED"
	exit 0
fi
echo "SOME TESTS FAILED"
exit 1
