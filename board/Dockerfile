# Build context is the repo root (so contracts/ is available to board's
# go.mod replace ../contracts). Invoke as:
#   docker build -f board/Dockerfile -t blerg-board .
# from the repo root.

# Stage 1: Go binary
FROM golang:1.27-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS go-builder
WORKDIR /src
COPY contracts/ /src/contracts/
COPY board/ /src/board/
WORKDIR /src/board
RUN go mod download
# The build context is a COPY, not a git checkout, so `git describe` can
# never succeed here (M6) — pass the version in explicitly instead.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=${VERSION}" -o blerg-board-server ./cmd/blerg-board-server && \
    CGO_ENABLED=0 GOOS=linux go build -o blerg-board ./cmd/blerg-board

# Stage 2: SPA
FROM node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS web-builder
WORKDIR /web
COPY board/web/package.json board/web/package-lock.json ./
RUN npm ci
COPY board/web/ ./
# Build-time override for where blerg-core is served from (see
# board/web/src/authClient.ts's coreOrigin). Left empty, the derivation covers
# both shapes this repo ships (k8s apex domain, desktop localhost:8081).
ARG VITE_CORE_URL=""
ARG VITE_CORE_PORT=""
ENV VITE_CORE_URL=$VITE_CORE_URL
ENV VITE_CORE_PORT=$VITE_CORE_PORT
RUN npm run build

# Stage 3: runtime
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /app
COPY --from=go-builder /src/board/blerg-board-server ./blerg-board-server
COPY --from=web-builder /web/dist ./web/dist
ENV BLERG_BOARD_WEB_DIR=/app/web/dist
EXPOSE 8080
ENTRYPOINT ["./blerg-board-server"]
