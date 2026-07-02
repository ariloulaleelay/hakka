.PHONY: build generate test cover run lint clean

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
	./$(BIN) --web-addr :8080 --ws-addr "" --log-level info --config hakka.json $(if $(DB),--db $(DB),)

failsafe-run: build
	./$(BIN) --log-level debug --config hakka.json

lint:
	go vet ./...

clean:
	rm -rf $(BIN) agent/webfront/webfront-dist

webfront-dist:
	cp -r ../hakka-webfront/dist ./agent/webfront/webfront-dist

