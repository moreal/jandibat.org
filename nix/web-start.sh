#!/bin/sh
set -eu

/bin/sh /etc/nginx/runtime-config.sh /tmp
exec /bin/nginx -e /dev/stderr -c /etc/nginx/nginx.conf -g 'daemon off;'
