# Audiobook Library Manager

BINARY := audiobookrenamer
DIST   := dist

.PHONY: help
help:
	@echo "Targets:"
	@echo "  make web        build the React frontend into internal/webui/dist"
	@echo "  make build      build the frontend, then the Go binary into $(DIST)/"
	@echo "  make run        build web + run the server locally"
	@echo "  make dev        run the Go API and the Vite dev server together"
	@echo "  make test       go test ./..."
	@echo "  make fmt        gofmt -w on all Go sources"
	@echo "  make docker     build the Docker image"

.PHONY: web
web:
	cd web && npm install && npm run build

.PHONY: build
build: web
	mkdir -p $(DIST)
	go build -o $(DIST)/$(BINARY) ./cmd/audiobookrenamer

.PHONY: run
run: web
	go run ./cmd/audiobookrenamer

.PHONY: dev
dev:
	@echo "Run these in two terminals:"
	@echo "  go run ./cmd/audiobookrenamer"
	@echo "  cd web && npm run dev   # http://localhost:5173"

.PHONY: test
test:
	go test ./...

.PHONY: fmt
fmt:
	gofmt -w ./cmd ./internal

VERSION ?= dev

.PHONY: docker
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) .

.PHONY: clean
clean:
	rm -rf $(DIST) internal/webui/dist/assets
