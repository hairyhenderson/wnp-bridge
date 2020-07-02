DOCKER_BUILDKIT ?= 1

all: docker

docker: Dockerfile
	@docker buildx build \
		--platform linux/arm/v6 \
		--platform linux/arm64 \
		--platform linux/amd64 \
		--push \
		--tag hairyhenderson/wnp-bridge .
