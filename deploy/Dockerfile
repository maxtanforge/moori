# Current control plane, including its Go-embedded browser UI.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG GITCOMMIT=unknown
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath \
    -ldflags "-s -w -X github.com/maxtanforge/moori/internal/version.Version=${VERSION} -X github.com/maxtanforge/moori/internal/version.GitCommit=${GITCOMMIT}" \
    -o /out/moori ./cmd/moori

FROM alpine:3.22 AS control
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 moori && adduser -S -u 10001 -G moori moori \
    && mkdir -m 0700 /data \
    && chown moori:moori /data
COPY --from=build /out/moori /usr/local/bin/moori
ENV MOORI_DATA_DIR=/data
WORKDIR /data
VOLUME /data
EXPOSE 8080
# Probe the local HTTP service.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["moori"]
CMD ["serve", "-local-execution", "off"]

# The default target includes the host Docker executor.
FROM control AS final
COPY --from=docker:28-cli /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose
RUN apk add --no-cache docker-cli \
    && docker --version \
    && docker --host unix:///var/run/docker.sock compose version
CMD ["serve"]
