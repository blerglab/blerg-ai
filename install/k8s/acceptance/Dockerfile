# Disposable smoke images only: build-tag gates, scripted engine, fixture CA and
# explicit hostpath admission overlay. NEVER promote to release tags.
ARG BASE_SERVER
ARG BASE_AGENT
FROM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
WORKDIR /src
COPY contracts/ /src/contracts/
COPY runner/ /src/runner/
COPY VERSION /src/VERSION
COPY install/k8s/acceptance/smoke-build.py /src/smoke-build.py
WORKDIR /src/runner
ENV GOWORK=off
ARG VERSION=acceptance
ARG COMMIT=unknown
RUN apt-get update && apt-get install -y --no-install-recommends python3 openssl && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /out && python3 /src/smoke-build.py /src/runner && \
    CGO_ENABLED=0 go build -tags blerg_workspace_integration -ldflags "-X main.version=${VERSION} -X main.release=$(cat /src/VERSION) -X main.commit=${COMMIT}" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -overlay /out/smoke-overlay.json -tags blerg_workspace_integration -ldflags "-X main.version=${VERSION}" -o /out/pod ./cmd/runner && \
    CGO_ENABLED=0 go test -tags blerg_workspace_integration -c -o /out/server.test ./internal/server
# Ephemeral key stays in tests image. Only the public CA enters agent trust.
# Build all targets using the SAME cache; verify their CA digests before import.
RUN mkdir -p /out/tls && openssl req -x509 -newkey rsa:2048 -nodes -days 7 \
    -subj /CN=acceptance-smoke -addext subjectAltName=DNS:acceptance-smoke \
    -keyout /out/tls/key.pem -out /out/tls/cert.pem && chmod 0600 /out/tls/key.pem

FROM ${BASE_SERVER} AS server
COPY --from=build /out/server /app/blerg-runner-server

FROM ${BASE_AGENT} AS agent
USER root
COPY --from=build --chmod=755 /out/pod /usr/local/bin/blerg-runner-pod
COPY --from=build /out/tls/cert.pem /usr/local/share/ca-certificates/acceptance-smoke.crt
RUN update-ca-certificates
COPY --chmod=755 install/k8s/acceptance/scripted-engine.py /usr/local/bin/claude
ENV BLERG_RUNNER_CLAUDE_USAGE=0
USER agent

FROM ${BASE_AGENT} AS tests
USER root
COPY --from=build --chmod=755 /out/server.test /acceptance/server.test
COPY --from=build --chown=1000:1000 /out/tls /acceptance/tls
COPY --from=build /out/tls/cert.pem /usr/local/share/ca-certificates/acceptance-smoke.crt
RUN update-ca-certificates
COPY --chmod=755 install/k8s/acceptance/test-entry.py /acceptance/test-entry.py
USER agent
ENTRYPOINT ["python3", "/acceptance/test-entry.py"]
