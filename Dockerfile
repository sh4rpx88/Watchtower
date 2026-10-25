FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -o /out/panel ./cmd/panel && CGO_ENABLED=0 go build -o /out/agent ./cmd/agent

FROM debian:12-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=go /out/panel /out/agent /usr/local/bin/
COPY --from=web /src/web/dist web/dist
COPY templates/ templates/
EXPOSE 8080
CMD ["panel"]
