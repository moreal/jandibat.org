#!/bin/sh
set -eu

api_base_url=${JANDIBAT_API_BASE_URL:-}
runtime_dir=${1:-/tmp}

fail() {
  printf '%s\n' "40-runtime-config.sh: ERROR: $1" >&2
  exit 1
}

if [ ! -d "$runtime_dir" ] || [ ! -w "$runtime_dir" ]; then
  fail "runtime output directory must exist and be writable"
fi

api_origin=
if [ -n "$api_base_url" ]; then
  case "$api_base_url" in
    https://*) ;;
    *) fail "JANDIBAT_API_BASE_URL must be empty or an absolute HTTPS URL" ;;
  esac

  authority=${api_base_url#https://}
  authority=${authority%%/*}
  case "$authority" in
    ""|*"@"*|*"?"*|*"#"*)
      fail "JANDIBAT_API_BASE_URL must contain a host and no credentials, query, or fragment"
      ;;
  esac

  if ! printf '%s' "$authority" | LC_ALL=C grep -Eq '^([A-Za-z0-9.-]+|\[[A-Fa-f0-9:.]+\])(:[0-9]{1,5})?$'; then
    fail "JANDIBAT_API_BASE_URL authority is not a valid HTTPS host and optional port"
  fi

  port=
  case "$authority" in
    \[*\]:*) port=${authority##*:} ;;
    \[*\]) ;;
    *:*) port=${authority##*:} ;;
  esac
  if [ -n "$port" ] && ! awk -v port="$port" 'BEGIN { exit !(port + 0 >= 1 && port + 0 <= 65535) }'; then
    fail "JANDIBAT_API_BASE_URL port must be between 1 and 65535"
  fi

  if printf '%s' "$api_base_url" | LC_ALL=C grep -Eq '[[:space:][:cntrl:]"\\]'; then
    fail "JANDIBAT_API_BASE_URL contains forbidden characters"
  fi

  case "$api_base_url" in
    *"?"*|*"#"*)
      fail "JANDIBAT_API_BASE_URL must not contain a query or fragment"
      ;;
  esac

  api_origin="https://$authority"
fi

# Escape again at the serialization boundary even though URL validation above
# rejects the dangerous characters. This keeps JSON generation safe if the URL
# policy is relaxed independently in the future.
escaped_api_base_url=$(printf '%s' "$api_base_url" | sed 's/\\/\\\\/g; s/"/\\"/g')
umask 022
printf '{"apiBaseUrl":"%s"}\n' "$escaped_api_base_url" > "$runtime_dir/config.json"

csp_connect_sources="'self'"
csp_img_sources="'self' data:"
if [ -n "$api_origin" ]; then
  csp_connect_sources="'self' $api_origin"
  csp_img_sources="'self' data: $api_origin"
fi
printf '%s\n' \
  "add_header Content-Security-Policy \"default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self'; img-src $csp_img_sources; font-src 'self'; connect-src $csp_connect_sources; manifest-src 'self'; worker-src 'self'; upgrade-insecure-requests\" always;" \
  > "$runtime_dir/security-headers.conf"
