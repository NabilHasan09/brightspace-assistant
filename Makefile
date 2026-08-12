BIN := bin
CMDS := brightspace-mcp ingest chat

.PHONY: all build test lint fmt vet tidy clean $(CMDS)

all: build

build: $(CMDS)

$(CMDS):
	@mkdir -p $(BIN)
	go build -o $(BIN)/$@ ./cmd/$@

test:
	go test ./...

# Race detector on, since ingest --watch and the MCP server are both
# concurrent and the manifest has exactly one writer by design.
test-race:
	go test -race ./...

vet:
	go vet ./...

# Fails if anything is unformatted, rather than reformatting in place —
# so CI can gate on it.
fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

lint: fmt vet

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)
