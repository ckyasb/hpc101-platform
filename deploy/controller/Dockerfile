# Platform Controller image for hpc101-platform
# Builds the controller binary (cmd/controller/main.go).
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY deploy/controller/go.controller.work ./go.work
COPY pkg/lease/ ./pkg/lease/
COPY pkg/controller/ ./pkg/controller/
# pkg/runtime requires go >= 1.25 (Docker SDK transitive dep)
COPY cmd/controller/ ./cmd/controller/
WORKDIR /src/cmd/controller
RUN CGO_ENABLED=0 go build -o /controller .
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /controller /controller
EXPOSE 8080
USER 1000
ENTRYPOINT ["/controller"]
