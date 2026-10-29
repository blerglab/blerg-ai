# Build context is the repo root (so contracts/ is available to core's go.mod
# replace ../contracts). Invoke as:
#   docker build -f core/Dockerfile -t blerg-core .
# from the repo root.

# Stage 1: Go binary
FROM golang:1.26.9@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS build
WORKDIR /src
COPY . .
# The release version is the repository's VERSION file; the commit is passed in (the build
# context is a copy, so `git rev-parse` cannot run here). landing.Release/Commit report them.
ARG COMMIT=
RUN CGO_ENABLED=0 go build \
    -ldflags "-X github.com/blerglab/blerg-ai/core/internal/landing.release=$(tr -d '[:space:]' < VERSION) -X github.com/blerglab/blerg-ai/core/internal/landing.commit=${COMMIT}" \
    -o /blerg-core ./core/cmd/blerg-core

# Stage 2: SPA (core/web — the login page, /app and /settings).
#
# Without this stage the image shipped no core/web/dist at all, so webDistDir()'s
# default resolved to nothing and the whole login/refresh flow 404'd in every
# deployment built from this file.
#
# VITE_CORE_URL/VITE_CORE_PORT are core/web/src/authClient.ts's build-time
# overrides for deriving core's own origin (see coreOrigin there). Core's
# frontend is same-origin with core, so the derivation already works unset —
# they exist here for parity with board/runner and for unusual topologies.
FROM node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS web-builder
WORKDIR /src/core/web
COPY core/web/package.json core/web/package-lock.json ./
RUN npm ci
COPY core/web/ ./
COPY packages/settings/ /src/packages/settings/
ARG VITE_CORE_URL=""
ARG VITE_CORE_PORT=""
ENV VITE_CORE_URL=$VITE_CORE_URL
ENV VITE_CORE_PORT=$VITE_CORE_PORT
RUN npm run build

# Stage 3: runtime
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /app
COPY --from=build /blerg-core /blerg-core
COPY --from=web-builder /src/core/web/dist ./web/dist
# webDistDir() (core/cmd/blerg-core/main.go) reads this; the default "web/dist"
# is relative to the process's working directory, which is /app here.
ENV BLERG_CORE_WEB_DIST=/app/web/dist
# landing.Version() (core/internal/landing/landing.go) reads BLERG_VERSION;
# pass --build-arg VERSION=<tag> at build time (deploy.sh does this).
ARG VERSION=dev
ENV BLERG_VERSION=$VERSION
# Numeric, not "nonroot": kubelet can only verify runAsNonRoot against a UID.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/blerg-core"]
