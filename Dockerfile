FROM golang:1.13.6-alpine3.10 AS build

WORKDIR /src/wnp-bridge
COPY . .
RUN CGOENABLED=0 go build -o /bin/wnp-bridge

FROM alpine:3.11.3 AS runtime

COPY --from=build /bin/wnp-bridge /bin/wnp-bridge

CMD ["/bin/wnp-bridge"]
