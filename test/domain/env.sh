#!/bin/sh
# Prints, as NAME=value lines, what the own-domain test needs to know about
# the containers test/domain/run.sh started.
#   env $(test/domain/env.sh) go test -tags integration -run OwnDomain ./internal/install/
set -eu
NODE=xc-domain-node
PEBBLE=xc-domain-pebble
cd "$(dirname "$0")"

ip() {
  docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"
}

# The root Pebble's own HTTPS certificate is signed with: acme.sh on the node
# and the test both check Pebble's answers against it.
CA="${TMPDIR:-/tmp}/xc-domain-pebble-ca.pem"
docker cp "$PEBBLE:/test/certs/pebble.minica.pem" "$CA" >/dev/null

echo "XC_TEST_DOMAIN_NODE=$(../node/address.sh "$NODE")"
echo "XC_TEST_PEBBLE=$(ip "$PEBBLE")"
echo "XC_TEST_ACME_CA=$CA"
