#!/usr/bin/env bash
# Agent 独立二进制发布（设计 13 / M5）：systemd 部署方式用的更小产物。
# 产出 dist/release/ 下双架构 tar.gz + SHA256SUMS + sizes.txt（体积记录
# 与来源信息）。纯 Go 交叉编译（CGO_ENABLED=0），无需 docker。
# 镜像构建走 make release-images（需要 docker buildx，见 docs/deployment.md）。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
GITCOMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
GO_VERSION=$(go version | awk '{print $3}')
OUT=dist/release
LDFLAGS="-s -w -X github.com/maxtanforge/moori/internal/version.Version=${VERSION} -X github.com/maxtanforge/moori/internal/version.GitCommit=${GITCOMMIT}"

rm -rf "$OUT"
mkdir -p "$OUT"

for arch in amd64 arm64; do
  name="moori-agent-${VERSION}-linux-${arch}"
  bin_path="$OUT/$name"
  GOOS=linux GOARCH=$arch CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$bin_path" ./cmd/moori-agent
  tar -czf "$OUT/$name.tar.gz" -C "$OUT" "$name"
  rm -f "$bin_path"
done

# 校验和与来源信息（设计 13：发布包含校验和与来源信息）。
cd "$OUT"
: > SHA256SUMS
for f in *.tar.gz; do
  sha256sum "$f" >> SHA256SUMS
done
{
  echo "version: $VERSION"
  echo "commit: $GITCOMMIT"
  echo "built_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "toolchain: $GO_VERSION"
  echo
  for f in *.tar.gz; do
    printf '%-48s %10s bytes (gzip)  /  %s bytes (binary)\n' "$f" \
      "$(stat -c%s "$f")" "$(gzip -dc "$f" | wc -c)"
  done
} > sizes.txt

echo "release artifacts in $OUT:"
ls -la
