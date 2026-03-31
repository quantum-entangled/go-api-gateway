.PHONY: run build test coverage lint clean infra-up infra-down migrate-up migrate-down bench

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
	docker compose up -d

infra-down:
	docker compose down

migrate-up:
	goose -dir migrations postgres "$$DATABASE_URL" up

migrate-down:
	goose -dir migrations postgres "$$DATABASE_URL" down

bench:
	go test -bench=. -benchmem ./...
