#!/bin/sh
# Builds and starts the test node, then prints how to reach it.
#   test/node/run.sh [name] [base-image]
set -eu
NAME="${1:-xc-node}"
BASE="${2:-debian:12}"
cd "$(dirname "$0")"
docker build -q --build-arg "BASE=$BASE" -t "xc-test-node:$(echo "$BASE" | tr ':/' '--')" . >/dev/null
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" --hostname "$NAME" --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  "xc-test-node:$(echo "$BASE" | tr ':/' '--')" >/dev/null
IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$NAME")"
echo "node:     $NAME"
echo "address:  $IP  (port 22)"
echo "login:    root / xc-test-root"
