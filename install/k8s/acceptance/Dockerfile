# Test-only overlay: never promote any of these images to a release tag.
ARG BASE_SERVER
ARG BASE_AGENT
FROM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
WORKDIR /src
COPY contracts/ /src/contracts/
COPY runner/ /src/runner/
COPY VERSION /src/VERSION
WORKDIR /src/runner
ENV GOWORK=off
ARG VERSION=acceptance
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -tags blerg_workspace_integration -ldflags "-X main.version=${VERSION} -X main.release=$(cat /src/VERSION) -X main.commit=${COMMIT}" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -tags blerg_workspace_integration -ldflags "-X main.version=${VERSION}" -o /out/pod ./cmd/runner && \
    CGO_ENABLED=0 go test -c -o /out/server.test ./internal/server

FROM ${BASE_SERVER} AS server
COPY --from=build /out/server /app/blerg-runner-server

FROM ${BASE_AGENT} AS agent
USER root
COPY --from=build --chmod=755 /out/pod /usr/local/bin/blerg-runner-pod
USER agent

FROM ${BASE_AGENT} AS tests
USER root
COPY --from=build --chmod=755 /out/server.test /acceptance/server.test
COPY --chmod=755 install/k8s/acceptance/test-entry.py /acceptance/test-entry.py
USER agent
ENTRYPOINT ["python3", "/acceptance/test-entry.py"]
