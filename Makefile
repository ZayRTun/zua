.PHONY: build install test vet fmt

build:
	go build -o bin/ ./cmd/...

# Install zua (TUI) and zua-agent (headless) into $(go env GOPATH)/bin.
install:
	go install ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .
