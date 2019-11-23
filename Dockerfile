FROM golang:1.13.4-alpine3.10 AS build

WORKDIR /src/wnp-bridge
COPY . .
RUN CGOENABLED=0 go build -o /bin/wnp-bridge

FROM alpine:3.10.3 AS runtime

COPY --from=build /bin/wnp-bridge /bin/wnp-bridge

CMD ["/bin/wnp-bridge"]
