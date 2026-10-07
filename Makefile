GO ?= go

.PHONY: test build clean

test:
	$(GO) test ./...
	$(GO) vet ./...
	sh -n install.sh uninstall.sh
	sh tests/install_test.sh

build: test
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build -trimpath -ldflags="-s -w" -o dist/web-inbox-linux-armv7 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o dist/web-inbox-linux-arm64 .
	cd dist && shasum -a 256 web-inbox-linux-armv7 web-inbox-linux-arm64 > SHA256SUMS

clean:
	rm -rf dist
