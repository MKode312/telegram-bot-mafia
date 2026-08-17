FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
	&& adduser -D -H -u 65532 bot

WORKDIR /app

COPY --from=builder /out/bot /app/bot
COPY config /app/config

USER bot

ENTRYPOINT ["/app/bot"]
