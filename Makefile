.PHONY: build test sqlc migration docker-up docker-down logs fmt vet
build:
	go build ./cmd/api
test:
	go test ./...
sqlc:
	sqlc generate
migration:
	@test -n "$(name)" || (echo "usage: make migration name=description" && exit 1)
	goose -dir db/migrations create $(name) sql
docker-up:
	docker compose up -d --build
docker-down:
	docker compose down
logs:
	docker compose logs -f backend
fmt:
	gofmt -w cmd internal
vet:
	go vet ./...
