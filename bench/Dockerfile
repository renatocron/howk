FROM golang:1.24-alpine AS builder

WORKDIR /build
COPY stress-test.go .
RUN go build -o stress-test stress-test.go

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /build/stress-test /usr/local/bin/stress-test
ENTRYPOINT ["stress-test"]
