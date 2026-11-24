VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build web test node integration integration-chain integration-domain run release clean

# One binary with the web interface inside.
build: web
	go build -ldflags "$(LDFLAGS)" -o bin/xray-console ./cmd/xray-console

# Builds the frontend into internal/webui/dist, where `go build` embeds it.
# The touch restores the placeholder Vite removes when it empties the folder.
web:
	cd web && pnpm install --frozen-lockfile && pnpm build
	touch internal/webui/dist/.gitkeep

test:
	go vet ./...
	go test ./...
	cd web && pnpm lint && pnpm test

# A throwaway Debian "VPS" in Docker for the installer to work on.
node:
	./test/node/run.sh xc-node

# The real installer against the real node: install, fail, roll back, install
# again. Needs Docker and GitHub. If the node's resolver hands out placeholder
# addresses for www.gstatic.com (a VPN client in fake-IP mode on this machine
# does that), set XC_TEST_CHECK_URL to an address it resolves honestly.
integration: node
	XC_TEST_NODE=$$(./test/node/address.sh xc-node) \
		go test -tags integration -count=1 -timeout 600s ./internal/sshx/ ./internal/xray/ ./internal/install/

# The chain test needs three nodes: an entry and two exits.
integration-chain:
	./test/node/run.sh xc-node-1
	./test/node/run.sh xc-node-2
	./test/node/run.sh xc-node-3
	XC_TEST_NODES=$$(./test/node/address.sh xc-node-1),$$(./test/node/address.sh xc-node-2),$$(./test/node/address.sh xc-node-3) \
		go test -tags integration -count=1 -timeout 900s -run Chain ./internal/install/

# A server that masquerades as its own domain: a node and Pebble — Let's
# Encrypt's test certificate authority — on a Docker network of their own, so
# the stub site and its certificate are set up for real without a real domain.
integration-domain:
	./test/domain/run.sh
	env $$(./test/domain/env.sh) \
		go test -tags integration -count=1 -timeout 1500s -run OwnDomain ./internal/install/

run: build
	./bin/xray-console serve --data ./data

# Static Linux binaries for the server the panel will live on.
release: web
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/xray-console-linux-amd64 ./cmd/xray-console
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/xray-console-linux-arm64 ./cmd/xray-console

clean:
	rm -rf bin
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
