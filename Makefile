APP=barber-back
VERSION ?= dev
IMAGE ?= ghcr.io/jcrlabs/$(APP):$(VERSION)
TEST_DATABASE_URL ?=

.PHONY: run build test test-integration lint fmt fmt-check vet tidy-check keys docker-build check

run:
	set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/server

build:
	go build -o bin/server ./cmd/server

test:
	go test -race ./...

# Requiere un Postgres de usar y tirar: TEST_DATABASE_URL=postgres://... make test-integration
test-integration:
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -race -count=1 ./internal/server/...

lint:
	golangci-lint run --timeout=5m

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed"; gofmt -l .; exit 1)

vet:
	go vet ./...

tidy-check:
	go mod tidy
	git diff --exit-code go.mod go.sum

# Clave RSA para firmar JWT en local (nunca se commitea)
keys:
	mkdir -p .keys && openssl genrsa -out .keys/jwt.pem 2048 && chmod 600 .keys/jwt.pem

docker-build:
	docker build -t $(IMAGE) .

check: fmt-check vet lint test
