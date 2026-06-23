#!/bin/bash
# Build CLI binaries for all platforms
set -e

VERSION="${1:-0.1.0}"
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-s -w -X paas-cli/cmd.version=${VERSION} -X paas-cli/cmd.commit=${COMMIT} -X paas-cli/cmd.date=${DATE}"
OUT_DIR="dist"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

platforms=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
)

for platform in "${platforms[@]}"; do
  GOOS="${platform%/*}"
  GOARCH="${platform#*/}"
  output="ghayma-${GOOS}-${GOARCH}"
  if [ "$GOOS" = "windows" ]; then
    output="${output}.exe"
  fi

  echo "Building ${GOOS}/${GOARCH}..."
  GOOS=$GOOS GOARCH=$GOARCH go build -ldflags "$LDFLAGS" -o "${OUT_DIR}/${output}" .
done

# Create tar.gz for unix, zip for windows
cd "$OUT_DIR"
for f in ghayma-linux-* ghayma-darwin-*; do
  [ -f "$f" ] || continue
  chmod +x "$f"
  tar -czf "${f}.tar.gz" "$f"
  rm "$f"
done

for f in ghayma-windows-*.exe; do
  [ -f "$f" ] || continue
  zip "${f%.exe}.zip" "$f"
  rm "$f"
done

echo ""
echo "Built artifacts:"
ls -lh
echo ""
echo "Ready for GitHub release v${VERSION}"
