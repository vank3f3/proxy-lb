FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o lb-server ./cmd/server

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/lb-server .
COPY --from=builder /app/configs/config.example.yaml /etc/lb/config.example.yaml

EXPOSE 8080
VOLUME ["/etc/lb"]
CMD ["./lb-server", "-config", "/etc/lb/config.yaml"] 