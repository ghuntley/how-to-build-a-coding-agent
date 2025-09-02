.PHONY: build fmt check clean all

# Go binaries to build
BINARIES := bash_tool chat edit_tool list_files read server

# Build all binaries
build:
	@echo "Building binaries..."
	go build -o bash_tool bash_tool.go
	go build -o chat chat.go
	go build -o edit_tool edit_tool.go
	go build -o list_files list_files.go
	go build -o read read.go
	go build -o server server.go

# Format all Go files
fmt:
	@echo "Formatting Go files..."
	go fmt ./...

# Check (lint and vet) all Go files
check:
	@echo "Running go vet on individual files..."
	go vet bash_tool.go
	go vet chat.go
	go vet edit_tool.go
	go vet list_files.go
	go vet read.go
	@echo "Running go mod tidy..."
	go mod tidy

# Clean built binaries
clean:
	@echo "Cleaning binaries..."
	rm -f $(BINARIES)

# Build everything and run checks
all: fmt check build

# Default target
.DEFAULT_GOAL := all
