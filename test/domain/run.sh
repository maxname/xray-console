#!/bin/sh
# Starts what the own-domain test needs, on a Docker network of its own: a
# test node that answers to two domain names, and Pebble — Let's Encrypt's
# test certificate authority — which checks those names over HTTP-01 the way
# the real one does. Nothing here leaves the machine or needs a real domain.
#   test/domain/run.sh
set -eu
NET=xc-domain
NODE=xc-domain-node
PEBBLE=xc-domain-pebble
PEBBLE_IMAGE="${XC_TEST_PEBBLE_IMAGE:-ghcr.io/letsencrypt/pebble:2.10.3}"
BASE="${1:-debian:12}"
IMAGE="xc-test-node:$(echo "$BASE" | tr ':/' '--')"
cd "$(dirname "$0")"

docker build -q --build-arg "BASE=$BASE" -t "$IMAGE" ../node >/dev/null
docker rm -f "$NODE" "$PEBBLE" >/dev/null 2>&1 || true
docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null

# The node is both names; a name that leads to Pebble itself stands for a
# domain whose DNS record points at somebody else's machine.
docker run -d --name "$NODE" --hostname "$NODE" --privileged --cgroupns=host \
  --network "$NET" --network-alias stub.xc-test.example --network-alias other.xc-test.example \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  "$IMAGE" >/dev/null

# PEBBLE_VA_NOSLEEP: do not wait between a challenge and its check.
# PEBBLE_WFE_NONCEREJECT=0: do not refuse valid requests at random.
docker run -d --name "$PEBBLE" --network "$NET" \
  --network-alias pebble --network-alias elsewhere.xc-test.example \
  -e PEBBLE_VA_NOSLEEP=1 -e PEBBLE_WFE_NONCEREJECT=0 \
  -v "$PWD/pebble-config.json:/test/config/xc.json:ro" \
  "$PEBBLE_IMAGE" -config /test/config/xc.json >/dev/null

echo "node:    $NODE  (stub.xc-test.example, other.xc-test.example)"
echo "pebble:  $PEBBLE  (https://pebble:14000/dir on the network $NET)"
