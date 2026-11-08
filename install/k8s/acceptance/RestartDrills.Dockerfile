# Fixture only: the Go Claude wire stub has no model implementation or credentials.
# Supply the daemon source through the daemon-source named build context.
FROM golang:1.26.9-bookworm AS fixture
RUN apt-get update && apt-get install -y --no-install-recommends git tmux gcc libc6-dev ca-certificates && rm -rf /var/lib/apt/lists/*
COPY contracts /src/contracts
COPY runner /src/runner
COPY VERSION /src/VERSION
COPY --from=daemon-source /contracts /daemon-source/contracts
COPY --from=daemon-source /runner /daemon-source/runner
COPY --from=daemon-source /VERSION /daemon-source/VERSION
ENV GOWORK=off GOCACHE=/var/cache/go GOMODCACHE=/go/pkg/mod GOMAXPROCS=1 GOFLAGS=-p=1
WORKDIR /src/runner
RUN go build -o /usr/local/bin/restart-drills ./cmd/restart-drills && \
    go test -run '^$' ./internal/daemon ./internal/server && \
    ln -s /usr/local/bin/restart-drills /usr/local/bin/claude
WORKDIR /daemon-source/runner
ARG DAEMON_COMMIT=fixture-source
RUN go build -ldflags "-X main.version=$(cat /daemon-source/VERSION) -X main.commit=${DAEMON_COMMIT}" -o /usr/local/bin/blerg-runner-daemon ./cmd/daemon
LABEL org.opencontainers.image.revision=${DAEMON_COMMIT}
WORKDIR /src/runner
RUN mkdir -p /data /var/cache/go && chown -R 1000:1000 /data /var/cache/go /go /src/runner
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/restart-drills"]
