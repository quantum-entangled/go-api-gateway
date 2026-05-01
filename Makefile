.PHONY: run build test coverage lint clean infra-up infra-down

run:
	go run ./cmd/gateway; true

build:
	go build -o bin/gateway ./cmd/gateway

test:
	go test -race -count=1 ./...

coverage:
	go test -race -coverprofile=cover.out ./...
	go tool cover -html=cover.out -o cover.html
	@echo "Coverage report: cover.html"

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ cover.out cover.html

infra-up:
	docker compose --env-file ./.env.example up -d

infra-down:
	docker compose --env-file ./.env.example down
