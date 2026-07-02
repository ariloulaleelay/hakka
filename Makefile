.PHONY: build test cover run lint clean

BIN := hakka
DB ?= hakka.db

build:
	go build -o $(BIN) ./cmd/hakka

test:
	go test ./...

cover:
	go test -cover ./...

debug-run: build
	./$(BIN) --llm-debug logs --log-level debug --config hakka.json $(if $(DB),--db $(DB),)

run: build
	./$(BIN) --log-level info --config hakka.json $(if $(DB),--db $(DB),)

failsafe-run: build
	./$(BIN) --log-level debug --config hakka.json

lint:
	go vet ./...

clean:
	rm -rf $(BIN)
