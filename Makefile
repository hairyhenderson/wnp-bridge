DOCKER_BUILDKIT ?= 1

all: docker
		# --platform linux/arm/v6 \
		# --platform linux/arm64 \

docker: Dockerfile
	@docker buildx build \
		--platform linux/amd64 \
		--push \
		--tag hairyhenderson/wnp-bridge .
