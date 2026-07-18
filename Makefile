BINARY := bin/gobalance
MOCK   := bin/mockbackend

.PHONY: help
help:
	@echo "GoBalance - week 1"
	@echo ""
	@echo "  make tidy       download dependencies (run this first)"
	@echo "  make build      build the load balancer and the mock backend"
	@echo "  make backends   start 3 mock backends on :9001 :9002 :9003"
	@echo "  make run        run the load balancer with gobalance.yaml"
	@echo "  make smoke      send 12 requests and show which backend served each"
	@echo "  make test       go test ./..."
	@echo "  make race       go test -race ./...  <- the one that matters"
	@echo "  make fmt        gofmt -w ."
	@echo "  make vet        go vet ./..."
	@echo "  make stop       kill the mock backends"

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: build
build:
	go build -o $(BINARY) ./cmd/gobalance
	go build -o $(MOCK) ./bench/mockbackend

.PHONY: run
run:
	go run ./cmd/gobalance -config gobalance.yaml -log-level info

.PHONY: backends
backends: build
	@bash scripts/run-backends.sh

.PHONY: stop
stop:
	@pkill -f "$(MOCK)" 2>/dev/null || true
	@echo "mock backends stopped"

.PHONY: smoke
smoke:
	@bash scripts/smoke.sh

.PHONY: test
test:
	go test ./...

.PHONY: race
race:
	go test -race ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: vet
vet:
	go vet ./...

.PHONY: check
check: fmt vet race
