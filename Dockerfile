FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 go build -o orbit ./cmd/server

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/orbit .
COPY sdk/js ./sdk/js
EXPOSE 8080
CMD ["./orbit"]
