.PHONY: build test cover run lint clean webfront docker-build

BIN := hakka
DB ?= hakka.db

build:
	go build -o $(BIN) ./cmd/hakka

hakka-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o hakka-amd64 ./cmd/hakka

test:
	go test ./...

cover:
	go test -cover ./...

debug-run: build
	./$(BIN) --llm-debug logs --log-level debug --ws-addr "" --web-addr :8080 --config hakka.json $(if $(DB),--db $(DB),)

run: build
	./$(BIN) --web-addr :8080 --ws-addr "" --log-level info --config hakka.json $(if $(DB),--db $(DB),)

lint:
	go vet ./...

clean:
	rm -rf $(BIN) agent/webfront/webfront-dist

webfront:
	rm -rf ./agent/webfront/webfront-dist
	cp -r ../hakka-webfront/dist ./agent/webfront/webfront-dist

