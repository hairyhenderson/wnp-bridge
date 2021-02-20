DOCKER_BUILDKIT ?= 1

all: docker
		# --platform linux/arm/v6 \
		# --platform linux/arm64 \
		# --platform linux/amd64

docker: Dockerfile
	@docker buildx build \
		--platform linux/amd64 \
		--platform linux/arm64 \
		--push \
		--tag hairyhenderson/wnp-bridge .

test:
	@go test -v -race -coverprofile=c.out ./...

lint:
	@golangci-lint run --verbose --max-same-issues=0 --max-issues-per-linter=0

ci-lint:
	@golangci-lint run --verbose --max-same-issues=0 --max-issues-per-linter=0 --out-format=github-actions

.PHONY: test lint ci-lint
.DELETE_ON_ERROR:
.SECONDARY:
