#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${VERSION:-1.1.0}
VERSION=${VERSION#v}
case "$VERSION" in
    ''|*[!0-9.]*) VALID_VERSION=0 ;;
    *) VALID_VERSION=1 ;;
esac
if [ "$VALID_VERSION" -ne 1 ] || ! printf '%s\n' "$VERSION" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
    echo "VERSION must be a stable semantic version such as 1.0.0" >&2
    exit 1
fi

COMMIT=${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILD_TIME=${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
BASE_LDFLAGS="-s -w -X mailmanager/internal/version.Version=$VERSION -X mailmanager/internal/version.Commit=$COMMIT -X mailmanager/internal/version.BuildTime=$BUILD_TIME"
DIST="$ROOT/dist"

if [ "${SKIP_NPM_CI:-0}" != "1" ]; then
    npm ci --prefix "$ROOT/web"
fi
npm run build --prefix "$ROOT/web"
rm -rf -- "$DIST"
mkdir -p "$DIST"

AMD64_LDFLAGS="$BASE_LDFLAGS -X mailmanager/internal/version.BuildMarker=mailmanager-release:$VERSION:linux:amd64"
ARM64_LDFLAGS="$BASE_LDFLAGS -X mailmanager/internal/version.BuildMarker=mailmanager-release:$VERSION:linux:arm64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -C "$ROOT" -trimpath -ldflags "$AMD64_LDFLAGS" -o dist/mailmanager-linux-amd64 ./cmd/mailmanager
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -C "$ROOT" -trimpath -ldflags "$ARM64_LDFLAGS" -o dist/mailmanager-linux-arm64 ./cmd/mailmanager

(
    unset CGO_ENABLED GOOS GOARCH
    go run -C "$ROOT" ./scripts/verify-release.go "$DIST/mailmanager-linux-amd64" "$VERSION" linux amd64
    go run -C "$ROOT" ./scripts/verify-release.go "$DIST/mailmanager-linux-arm64" "$VERSION" linux arm64
)

cp "$ROOT/deploy/mailmanager.service" "$DIST/mailmanager.service"
cp "$ROOT/deploy/mailmanager-updater.service" "$DIST/mailmanager-updater.service"
cp "$ROOT/deploy/mailmanager-updater.path" "$DIST/mailmanager-updater.path"
cp "$ROOT/deploy/mailmanager.env.example" "$DIST/mailmanager.env.example"
cp "$ROOT/deploy/nginx.conf.example" "$DIST/nginx.conf.example"
cp "$ROOT/scripts/install-linux.sh" "$DIST/install-linux.sh"

cd "$DIST"
set -- \
    install-linux.sh \
    mailmanager.env.example \
    mailmanager-linux-amd64 \
    mailmanager-linux-arm64 \
    mailmanager-updater.path \
    mailmanager-updater.service \
    mailmanager.service \
    nginx.conf.example
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@" | LC_ALL=C sort -k2 > checksums.txt
elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$@" | LC_ALL=C sort -k2 > checksums.txt
else
    echo "sha256sum or shasum is required" >&2
    exit 1
fi
