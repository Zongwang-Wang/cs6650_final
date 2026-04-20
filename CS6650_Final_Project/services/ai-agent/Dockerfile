# Pre-compiled binary — build with:
# CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ai-agent .
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY ai-agent .
EXPOSE 8083
ENTRYPOINT ["./ai-agent"]
