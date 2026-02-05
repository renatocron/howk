# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build all binaries
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o bin/howk-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o bin/howk-worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o bin/howk-scheduler ./cmd/scheduler
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o bin/howk-reconciler ./cmd/reconciler

# Final stage - API
FROM scratch AS api
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /build/bin/howk-api /howk-api
COPY --from=builder /build/config.example.yaml /config.yaml
EXPOSE 8080
ENTRYPOINT ["/howk-api"]

# Final stage - Worker
FROM scratch AS worker
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /build/bin/howk-worker /howk-worker
COPY --from=builder /build/config.example.yaml /config.yaml
ENTRYPOINT ["/howk-worker"]

# Final stage - Scheduler
FROM scratch AS scheduler
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /build/bin/howk-scheduler /howk-scheduler
COPY --from=builder /build/config.example.yaml /config.yaml
ENTRYPOINT ["/howk-scheduler"]

# Final stage - Reconciler
FROM scratch AS reconciler
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /build/bin/howk-reconciler /howk-reconciler
COPY --from=builder /build/config.example.yaml /config.yaml
ENTRYPOINT ["/howk-reconciler"]
