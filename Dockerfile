# syntax=docker/dockerfile:1.1.7-experimental
FROM golang:1.15.2-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /src/wnp-bridge
COPY go.mod .
COPY go.sum .

RUN --mount=type=cache,id=go-build-${TARGETOS}-${TARGETARCH}${TARGETVARIANT},target=/root/.cache/go-build \
    --mount=type=cache,id=go-pkg-${TARGETOS}-${TARGETARCH}${TARGETVARIANT},target=/go/pkg \
        go mod download -x

COPY . .

RUN --mount=type=cache,id=go-build-${TARGETOS}-${TARGETARCH}${TARGETVARIANT},target=/root/.cache/go-build \
    --mount=type=cache,id=go-pkg-${TARGETOS}-${TARGETARCH}${TARGETVARIANT},target=/go/pkg \
        CGOENABLED=0 go build -o /bin/wnp-bridge

FROM alpine:3.12.0 AS runtime

COPY --from=build /bin/wnp-bridge /bin/wnp-bridge

CMD ["/bin/wnp-bridge"]
