FROM golang:1.22-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o llamactl ./cmd/llamactl

FROM debian:trixie-slim
COPY --from=builder /app/llamactl /usr/local/bin/llamactl
VOLUME ["/data"]
EXPOSE 3000
ENTRYPOINT ["llamactl"]
