# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY *.go ./

RUN go build -ldflags="-s -w" -o ti-temp-mail-api .

# Final stage
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/ti-temp-mail-api .

EXPOSE 8080
EXPOSE 25

ENTRYPOINT ["./ti-temp-mail-api"]
