#!/usr/bin/env bash
set -euo pipefail
base="http://127.0.0.1:8000"
code() { curl -s -o /dev/null -w "%{http_code}" "$@"; }

# maintenance active, non-whitelisted client -> blocked (512)
got=$(code -H "Host: on.localhost" -H "Cf-Connecting-Ip: 198.51.100.9" "$base/")
[ "$got" = "512" ] || { echo "active/blocked: want 512 got $got"; exit 1; }

# maintenance active, whitelisted client -> passes (200 from whoami)
got=$(code -H "Host: on.localhost" -H "Cf-Connecting-Ip: 203.0.113.7" "$base/")
[ "$got" = "200" ] || { echo "active/whitelisted: want 200 got $got"; exit 1; }

# maintenance inactive -> passes (200)
got=$(code -H "Host: off.localhost" -H "Cf-Connecting-Ip: 198.51.100.9" "$base/")
[ "$got" = "200" ] || { echo "inactive: want 200 got $got"; exit 1; }

echo "e2e: all assertions passed"
