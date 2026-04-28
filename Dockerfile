# ── Stage 1: Build ─────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Bağımlılıkları önce indir (cache için)
COPY go.mod go.sum ./
RUN go mod download

# Kaynak kodunu kopyala
COPY . .

# Statik binary derle
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o server .

# ── Stage 2: Runtime ────────────────────────────────────────────────────────
FROM alpine:3.20

WORKDIR /app

# Güvenlik: root olmayan kullanıcı
RUN adduser -D -u 10001 appuser

COPY --from=builder /app/server /app/server

USER appuser

# Cloud Run PORT env variable ile gelir
ENV PORT=8080
EXPOSE 8080

CMD ["/app/server"]
