# Build CSOJ from vendored source tree (vendor/csoj/)
# Used by the hpc101-platform CSOJ deployment.
#
# Usage:
#   docker build -f deploy/csoj/Dockerfile -t hpc101-platform/csoj:latest .
#   (build context = repo root, so vendor/csoj/ is accessible)
# Stage 1: Build CSOJ binary from vendored source
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache gcc musl-dev sqlite-dev
WORKDIR /src
COPY vendor/csoj/ ./
RUN CGO_ENABLED=1 go build -o /csoj ./cmd/CSOJ/

# Stage 2: Minimal runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata sqlite-libs
COPY --from=builder /csoj /csoj
RUN mkdir -p /data /contests /etc/csoj
EXPOSE 8080 8081
ENTRYPOINT ["/csoj", "-c", "/etc/csoj/config.yaml"]
