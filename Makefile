.PHONY: build test cover run lint clean

BIN := bin/hakka
DB ?= hakka.db

build:
	@mkdir -p bin
	go build -o $(BIN) ./cmd/hakka

test:
	go test ./...
	@echo ""
	@echo "--- nvim/lua tests ---"
	@cd nvim && nvim --headless -c "lua dofile('tests/run.lua')" 2>&1

cover:
	go test -cover ./...

run: build
	$(BIN) --llm-debug logs --log-level debug --config hakka.json $(if $(DB),--db $(DB),)

lint:
	go vet ./...

clean:
	rm -rf bin
