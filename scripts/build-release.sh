#!/bin/sh
set -eu

npm run frontend:build
mkdir -p dist/release

build() {
  goos="$1"
  goarch="$2"
  output="dist/release/materialmind-${goos}-${goarch}"
  if [ "$goos" = "windows" ]; then
    output="${output}.exe"
  fi
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -tags embed_frontend -trimpath -ldflags="-s -w" -o "$output" ./cmd/materialmind
}

build linux amd64
build linux arm64
build darwin amd64
build darwin arm64
build windows amd64
build windows arm64

(
  cd dist/release
  sha256sum materialmind-* > checksums.txt
)

