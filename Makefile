.PHONY: run build test lint tidy fmt vet generate

# Regenerate Go model structs from openapi.yaml.
# Pinned to a fixed oapi-codegen version so regeneration is reproducible: the
# tool's enum-constant naming (collision prefixing) drifts across versions, so
# @latest would silently reshuffle generated constant names. Run via `go run`
# so no separate `go install` is needed.
OAPI_CODEGEN_VERSION := v2.5.1
generate:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) -config oapi-codegen.yaml openapi.yaml

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
