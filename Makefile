VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BINARY  := ghayma
LDFLAGS := -s -w -X paas-cli/cmd.version=$(VERSION) -X paas-cli/cmd.commit=$(COMMIT) -X paas-cli/cmd.date=$(DATE)

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: build clean release-all

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

release-all: clean
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		echo "Building $$os/$$arch..."; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$$os-$$arch$$ext . ; \
	done
	@echo "Done. Binaries in dist/"
	@ls -lh dist/

clean:
	rm -rf dist/
