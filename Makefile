.PHONY: run build test lint tidy fmt vet generate

# Regenerate Go model structs from openapi.yaml. Requires:
#   go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
generate:
	oapi-codegen -config oapi-codegen.yaml openapi.yaml

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./... -race -count=1

lint:
	go vet ./...

tidy:
	go mod tidy

fmt:
	go fmt ./...

vet:
	go vet ./...
