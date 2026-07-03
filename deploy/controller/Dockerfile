# Platform Controller image for hpc101-platform
# Builds the controller binary from the monorepo.
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY pkg/ ./
WORKDIR /src/controller
RUN CGO_ENABLED=0 go build -o /controller .
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /controller /controller
EXPOSE 8080
USER 1000
ENTRYPOINT ["/controller"]
