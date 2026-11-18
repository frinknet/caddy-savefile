#!/bin/sh
# End-to-end checks for the built image. Run ./build.sh first, then this script:
# it starts the image once and drives it entirely with tools that exist inside
# it (toybox sh, printf, nc, wget, grep), so nothing is needed on the host.
#
# The configuration under tests/e2e/config is mounted in read-only, so these
# checks are independent of the demo config in www/.
#
# Toybox sh has no `set -e`, so each block ends in the command whose status
# decides the check, and every socket operation is wrapped in `timeout`.
set -eu

IMAGE=${IMAGE:-caddy-savefile:test}
NAME=${NAME:-caddy-savefile-e2e}
PORT=8080
ROOT=/var/uploads

HERE=$(cd "$(dirname "$0")" && pwd)
CONFIG=$HERE/config

say() { printf 'ok: %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
run_in() { timeout 30 docker exec "$NAME" /bin/sh -c "$1"; }
hits() { run_in "timeout 10 /bin/wget -O /tmp/$1.out http://127.0.0.1:$PORT$2 >/dev/null 2>&1"; }

timeout 20 docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "image $IMAGE not found; run ./build.sh first"
[ -d "$CONFIG" ] || fail "missing $CONFIG"

timeout 60 docker rm -f "$NAME" >/dev/null 2>&1 || true
trap 'timeout 60 docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT INT TERM

timeout 60 docker run -d --name "$NAME" \
	-v "$CONFIG/Caddyfile:/etc/caddy/Caddyfile:ro" \
	-v "$CONFIG:/srv/e2e:ro" \
	"$IMAGE" >/dev/null

ready=0
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
	if hits probe /; then
		ready=1
		break
	fi
	sleep 1
done
if [ "$ready" != 1 ]; then
	timeout 30 docker logs "$NAME" 2>&1 | tail -20
	fail "server did not become ready"
fi
say "server is serving the mounted config"

# Provisioning prepares the directories, so root_dir must exist before any
# request has been made.
run_in "/bin/test -d $ROOT" || fail "root_dir was not created when the config loaded"
say "root_dir was prepared at config load"

hits index / || fail "GET / failed"
run_in '/bin/grep -q "Upload a submission" /tmp/index.out' || fail "index page was not served"
say "static index served"

hits page /upload || fail "GET /upload failed"
run_in '/bin/grep -q "Submission received from" /tmp/page.out' || fail "templates page did not render"
run_in "/bin/test ! -e $ROOT/receipts/alice-notes.txt" \
	|| fail "GET wrote a receipt; the template action must not fire without input"
say "templates page renders without side effects"

run_in 'printf "%s" "username=alice&notes=hello+there" > /tmp/url.body
len=$(/bin/wc -c < /tmp/url.body)
printf "POST /upload HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: %s\r\nConnection: close\r\n\r\n" "$len" > /tmp/url.req
/bin/cat /tmp/url.body >> /tmp/url.req
timeout 5 /bin/nc 127.0.0.1 '"$PORT"' < /tmp/url.req > /tmp/url.resp' || fail "urlencoded POST failed"
run_in '/bin/grep -q "200 OK" /tmp/url.resp' || fail "urlencoded POST was not accepted"
run_in '/bin/grep -q "Submission received from alice" /tmp/url.resp' || fail "form field did not reach the template"
run_in "/bin/test \"\$(/bin/cat $ROOT/receipts/alice-notes.txt)\" = \"hello there\"" \
	|| fail "the template savefile action wrote the wrong receipt"
say "urlencoded form -> formdata -> template -> file"

run_in "printf \"\\211PNG\\r\\n\\032\\n raw body, not multipart\" > /tmp/raw.body
len=\$(/bin/wc -c < /tmp/raw.body)
printf \"POST /upload HTTP/1.1\\r\\nHost: 127.0.0.1\\r\\nContent-Type: image/png\\r\\nContent-Length: %s\\r\\nConnection: close\\r\\n\\r\\n\" \"\$len\" > /tmp/raw.req
/bin/cat /tmp/raw.body >> /tmp/raw.req
timeout 5 /bin/nc 127.0.0.1 $PORT < /tmp/raw.req > /tmp/raw.resp" || fail "raw single-file POST failed"
run_in '/bin/grep -q "200 OK" /tmp/raw.resp' || fail "raw single-file POST was not accepted"
say "raw single-file POST accepted and routed by MIME type"

run_in 'b=----caddysavefile
{
	printf "%s\r\n" "--$b"
	printf "Content-Disposition: form-data; name=\"username\"\r\n\r\n"
	printf "bob\r\n"
	printf "%s\r\n" "--$b"
	printf "Content-Disposition: form-data; name=\"photo\"; filename=\"photo.png\"\r\n"
	printf "Content-Type: image/png\r\n\r\n"
	printf "\211PNG\r\n\032\n fake png bytes\r\n"
	printf "%s\r\n" "--$b"
	printf "Content-Disposition: form-data; name=\"doc\"; filename=\"doc.pdf\"\r\n"
	printf "Content-Type: application/pdf\r\n\r\n"
	printf "%%PDF-1.4 fake pdf bytes\r\n"
	printf "%s\r\n" "--$b"
	printf "Content-Disposition: form-data; name=\"clip\"; filename=\"clip.mp4\"\r\n"
	printf "Content-Type: video/mp4\r\n\r\n"
	printf "ignored bytes\r\n"
	printf "%s--\r\n" "--$b"
} > /tmp/multipart.body
len=$(/bin/wc -c < /tmp/multipart.body)
printf "POST /upload HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: multipart/form-data; boundary=%s\r\nContent-Length: %s\r\nConnection: close\r\n\r\n" "$b" "$len" > /tmp/multipart.req
/bin/cat /tmp/multipart.body >> /tmp/multipart.req
timeout 5 /bin/nc 127.0.0.1 '"$PORT"' < /tmp/multipart.req > /tmp/multipart.resp' || fail "multipart POST failed"
run_in '/bin/grep -q "200 OK" /tmp/multipart.resp' || fail "multipart POST was not accepted"
run_in '/bin/grep -q "Submission received from bob" /tmp/multipart.resp' || fail "multipart field did not reach the template"
say "multipart POST accepted"

run_in "/bin/test \"\$(/bin/ls -1 $ROOT/images | /bin/wc -l)\" = \"2\"" \
	|| fail "images/ should hold the raw upload and the multipart photo"
run_in "/bin/ls -1 $ROOT/images | /bin/grep -q \"_upload.png\$\"" || fail "raw upload was not named <uuid>_upload.png"
run_in "/bin/ls -1 $ROOT/images | /bin/grep -q \"_photo.png\$\"" || fail "multipart photo was not saved"
say "image/* rule stored both uploads"

run_in "/bin/test \"\$(/bin/ls -1 $ROOT/docs | /bin/wc -l)\" = \"1\"" || fail "docs/ should hold exactly the PDF"
run_in "/bin/ls -1 $ROOT/docs | /bin/grep -q \"_doc.pdf\$\"" || fail "PDF was not saved"
say "application/pdf rule stored the document"

run_in "/bin/test ! -e $ROOT/videos" || fail "unmatched video/mp4 part was written to disk"
run_in "/bin/test \"\$(/bin/ls -1 $ROOT | /bin/wc -l)\" = \"3\"" || fail "unexpected directories under root_dir"
say "unmatched MIME type was discarded"

run_in "printf \"<!DOCTYPE html><html><body>not an image</body></html>\" > /tmp/liar.body
len=\$(/bin/wc -c < /tmp/liar.body)
printf \"POST /upload HTTP/1.1\\r\\nHost: 127.0.0.1\\r\\nContent-Type: image/png\\r\\nContent-Length: %s\\r\\nConnection: close\\r\\n\\r\\n\" \"\$len\" > /tmp/liar.req
/bin/cat /tmp/liar.body >> /tmp/liar.req
timeout 5 /bin/nc 127.0.0.1 $PORT < /tmp/liar.req > /tmp/liar.resp" || fail "mismatch request could not be sent"
run_in '/bin/grep -q "200 OK" /tmp/liar.resp' || fail "a page claiming image/png was rejected instead of discarded"
run_in "/bin/test \"\$(/bin/ls -1 $ROOT/images | /bin/wc -l)\" = \"2\"" \
	|| fail "a page claiming image/png was stored under an image/* rule"
run_in "/bin/test \"\$(/bin/ls -1 $ROOT | /bin/wc -l)\" = \"3\"" \
	|| fail "the mismatch left something under root_dir"
say "active content claiming image/png was discarded"

run_in 'b=----caddysavefile
{
	printf "%s\r\n" "--$b"
	for n in 1 2 3 4 5 6 7; do
		printf "Content-Disposition: form-data; name=\"field$n\"\r\n\r\n"
		printf "value\r\n"
		printf "%s\r\n" "--$b"
	done
	printf "%s--\r\n" "--$b"
} > /tmp/over.body
len=$(/bin/wc -c < /tmp/over.body)
printf "POST /upload HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: multipart/form-data; boundary=%s\r\nContent-Length: %s\r\nConnection: close\r\n\r\n" "$b" "$len" > /tmp/over.req
/bin/cat /tmp/over.body >> /tmp/over.req
timeout 5 /bin/nc 127.0.0.1 '"$PORT"' < /tmp/over.req > /tmp/over.resp || true' || fail "part-cap request could not be sent"
run_in '/bin/grep -q "413" /tmp/over.resp' || fail "a body past max_parts was not rejected with 413"
say "max_parts rejected an oversized part count"

run_in "printf \"POST /upload HTTP/1.1\\r\\nHost: 127.0.0.1\\r\\nContent-Type: image/png\\r\\nContent-Length: 4000000\\r\\nConnection: close\\r\\n\\r\\n\" > /tmp/big.req
printf \"01234567\" >> /tmp/big.req
timeout 5 /bin/nc 127.0.0.1 $PORT < /tmp/big.req > /tmp/big.resp || true" || fail "oversize request could not be sent"
run_in '/bin/grep -q "413" /tmp/big.resp' || fail "a declared oversize body was not rejected with 413"
say "max_size rejected an oversized body before reading it"

logs=$(timeout 30 docker logs "$NAME" 2>&1) || fail "could not read the container logs"
printf '%s' "$logs" | grep -q '"msg":"saved part"' || fail "a saved part was not logged"
printf '%s' "$logs" | grep -q '"msg":"discarded part"' || fail "a discarded part was not logged"
printf '%s' "$logs" | grep -q 'routing a part by its content' \
	|| fail "the active-content override was not logged"
printf '%s' "$logs" | grep -q '"msg":"parsed form body"' || fail "the form parse was not logged"
say "logs record parses, saves, discards and overrides"

printf '\nall image checks passed\n'