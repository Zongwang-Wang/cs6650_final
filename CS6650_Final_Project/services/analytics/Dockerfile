# Pre-compiled binary — build with: GOOS=linux GOARCH=amd64 go build -o analytics .
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY analytics .
EXPOSE 8081
ENTRYPOINT ["./analytics"]
