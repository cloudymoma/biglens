# Makefile for BigLens V1

.PHONY: build-frontend build-backend build-all serve test clean

# Variables
PORT=1983
BINARY_NAME=biglens-server

build-frontend:
	@echo "==> Building frontend..."
	cd frontend && npm install && npm run build
	@echo "==> Moving static files to backend..."
	mkdir -p backend/static
	rm -rf backend/static/*
	cp -r frontend/dist/* backend/static/

build-backend:
	@echo "==> Building Go backend..."
	mkdir -p bin
	cd backend && go build -o ../bin/$(BINARY_NAME) .

build-all: build-frontend build-backend
	@echo "==> BigLens built successfully."

serve: build-all
	@if [ ! -f conf.yaml ]; then \
		echo "==> conf.yaml not found, copying from template..."; \
		cp conf.yaml.template conf.yaml; \
	fi
	@echo "==> Launching BigLens on port $(PORT)..."
	./bin/$(BINARY_NAME)

test:
	@echo "==> Running tests..."
	cd backend && go test -v ./...
	cd frontend && npm run test

clean:
	@echo "==> Cleaning up..."
	rm -f bin/$(BINARY_NAME)
	rm -rf frontend/dist
