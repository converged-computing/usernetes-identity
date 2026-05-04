BINARY=usernetes-identity
VERSION=0.1.0
PKG=github.com/converged-computing/usernetes-identity

# Hardening: PIE (Position Independent Executable), Static linking, Trimpath
GOFLAGS=-trimpath -buildmode=pie
LDFLAGS=-ldflags "-s -w -extldflags '-static' -X main.version=${VERSION}"

all: build

# Note we need this library
# sudo apt-get update
# sudo apt-get install libseccomp-dev
build:
	mkdir -p ./bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -tags netgo,osusergo -trimpath -buildmode=pie -ldflags "-s -w -extldflags '-static' -X main.version=0.1.0" -o bin/usernetes-identity cmd/usernetes-identity/main.go
	
clean:
	rm -rf bin/