.PHONY: build run tidy test vet fmt

build:
	go build -o oidio ./cmd/oidio

run: build
	./oidio --config oidio.yaml

tidy:
	go mod tidy

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
