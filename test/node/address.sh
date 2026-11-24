#!/bin/sh
# Prints the test node's address once its SSH server answers.
#   test/node/address.sh [name]
set -eu
NAME="${1:-xc-node}"
IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$NAME")"
# The container is "running" a few seconds before sshd inside it is.
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  if docker exec "$NAME" systemctl is-active --quiet ssh 2>/dev/null; then
    echo "$IP"
    exit 0
  fi
  sleep 1
done
echo "test node $NAME did not start its SSH server" >&2
exit 1
