FROM golang:1.15.0-alpine AS build

WORKDIR /src/wnp-bridge
COPY . .
RUN CGOENABLED=0 go build -o /bin/wnp-bridge

FROM alpine:3.12.0 AS runtime

COPY --from=build /bin/wnp-bridge /bin/wnp-bridge

CMD ["/bin/wnp-bridge"]
