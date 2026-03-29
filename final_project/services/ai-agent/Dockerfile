FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ai-agent .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates terraform
WORKDIR /app
COPY --from=builder /app/ai-agent .
EXPOSE 8083
ENTRYPOINT ["./ai-agent"]
