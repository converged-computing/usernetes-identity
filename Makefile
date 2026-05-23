BINARY=usernetes-identity
BINARY_NRI := usernetes-identity-nri
VERSION=0.1.0

# Custom libseccomp installation path
SECCOMP_PREFIX=/usr/workspace/usernetes/install

# CGO configuration to point to the custom path
export CGO_CFLAGS=-I$(SECCOMP_PREFIX)/include
export CGO_LDFLAGS=-L$(SECCOMP_PREFIX)/lib

# Hardening and static linking flags
GOFLAGS=-tags netgo,osusergo -trimpath -buildmode=pie
LDFLAGS=-ldflags "-s -w -extldflags '-static' -X main.version=$(VERSION)"

all: build

build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY) cmd/$(BINARY)/main.go
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -mod=vendor $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY_NRI) cmd/$(BINARY_NRI)/main.go

clean:
	rm -rf bin/
