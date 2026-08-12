#!/bin/sh
set -eu

nginx_config="./nginx.conf"

grep -F 'access_log off;' "$nginx_config" >/dev/null
grep -F 'add_header Referrer-Policy "no-referrer" always;' "$nginx_config" >/dev/null

# Never reintroduce nginx variables that expose a magic-link query or Referer.
if grep -E '\$request([^_[:alnum:]]|$)|\$args([^_[:alnum:]]|$)|\$query_string([^_[:alnum:]]|$)|\$http_referer([^_[:alnum:]]|$)' "$nginx_config" >/dev/null; then
  echo "nginx logging must not contain request query or Referer variables" >&2
  exit 1
fi

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
entrypoint=./docker-entrypoint.d/40-runtime-config.sh

fail() {
  printf '%s\n' "runtime-config test failed: $1" >&2
  exit 1
}

valid_dir="$test_dir/valid"
mkdir "$valid_dir"
JANDIBAT_API_BASE_URL='https://api.example.test:8443/v1' sh "$entrypoint" "$valid_dir"
grep -Fx '{"apiBaseUrl":"https://api.example.test:8443/v1"}' "$valid_dir/config.json" >/dev/null
grep -F "connect-src 'self' https://api.example.test:8443;" "$valid_dir/security-headers.conf" >/dev/null
grep -F "img-src 'self' data: https://api.example.test:8443;" "$valid_dir/security-headers.conf" >/dev/null
if grep -F 'img-src' "$valid_dir/security-headers.conf" | grep -F ' https:;' >/dev/null; then
  fail "configured img-src still contains an arbitrary HTTPS source"
fi
if grep -F '/v1' "$valid_dir/security-headers.conf" >/dev/null; then
  fail "CSP contains an API path instead of only the origin"
fi

empty_dir="$test_dir/empty"
mkdir "$empty_dir"
JANDIBAT_API_BASE_URL='' sh "$entrypoint" "$empty_dir"
grep -Fx '{"apiBaseUrl":""}' "$empty_dir/config.json" >/dev/null
grep -F "connect-src 'self';" "$empty_dir/security-headers.conf" >/dev/null
grep -F "img-src 'self' data:;" "$empty_dir/security-headers.conf" >/dev/null
if grep -F 'img-src' "$empty_dir/security-headers.conf" | grep -F 'https://' >/dev/null; then
  fail "empty img-src unexpectedly contains an external origin"
fi

encoded_dir="$test_dir/encoded"
mkdir "$encoded_dir"
encoded_attack='https://api.example.test/%0d%0aadd_header%20X-Injected:%20yes'
JANDIBAT_API_BASE_URL="$encoded_attack" sh "$entrypoint" "$encoded_dir"
if grep -F 'X-Injected' "$encoded_dir/security-headers.conf" >/dev/null; then
  fail "encoded path data reached the generated CSP include"
fi
grep -F "img-src 'self' data: https://api.example.test;" "$encoded_dir/security-headers.conf" >/dev/null

newline_attack='https://api.example.test
add_header X-Injected yes;'
if JANDIBAT_API_BASE_URL="$newline_attack" sh "$entrypoint" "$test_dir" >/dev/null 2>&1; then
  fail "newline header injection was accepted"
fi

quote_attack='https://api.example.test/";add_header X-Injected yes;#'
if JANDIBAT_API_BASE_URL="$quote_attack" sh "$entrypoint" "$test_dir" >/dev/null 2>&1; then
  fail "quote-based Nginx config injection was accepted"
fi

if JANDIBAT_API_BASE_URL='https://api.example.test:99999' sh "$entrypoint" "$test_dir" >/dev/null 2>&1; then
  fail "an invalid port was accepted"
fi

if JANDIBAT_API_BASE_URL='https://api$variable.example.test' sh "$entrypoint" "$test_dir" >/dev/null 2>&1; then
  fail "an Nginx variable marker was accepted in the authority"
fi

printf '%s\n' 'runtime-config security tests passed'
