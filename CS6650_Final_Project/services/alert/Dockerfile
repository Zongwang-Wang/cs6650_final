# Pre-compiled binary — build with: GOOS=linux GOARCH=amd64 go build -o alert .
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY alert .
EXPOSE 8082
ENTRYPOINT ["./alert"]
