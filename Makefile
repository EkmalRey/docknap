# docknap Makefile
# Common development tasks.

GO        ?= go
PKG       := ./...
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X main.version=$(VERSION)
BIN       := docknap

.PHONY: all
all: vet test build

.PHONY: build
build:
	$(GO) build -trimpath -ldflags="-s -w $(LDFLAGS)" -o $(BIN) .

.PHONY: run
run: build
	./$(BIN)

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: test
test:
	$(GO) test -race -count=1 $(PKG)

.PHONY: cover
cover:
	$(GO) test -race -count=1 -coverprofile=coverage.txt -covermode=atomic $(PKG)
	$(GO) tool cover -func=coverage.txt | tail -1

.PHONY: cover-html
cover-html: cover
	$(GO) tool cover -html=coverage.txt -o coverage.html

.PHONY: fmt
fmt:
	gofmt -s -w .

.PHONY: docker
docker:
	docker build -t docknap:$(VERSION) -t docknap:latest --build-arg VERSION=$(VERSION) .

.PHONY: integration
integration:
	bash tests/integration/run.sh

.PHONY: clean
clean:
	rm -f $(BIN) coverage.txt coverage.html
