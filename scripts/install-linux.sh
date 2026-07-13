#!/usr/bin/env sh
set -eu

REPOSITORY="MengStar-L/MailManager"
VERSION=${MAILMANAGER_VERSION:-latest}
START_SERVICE=1
TARGET_DIR="/opt/mailmanager/bin"
TARGET_BINARY="$TARGET_DIR/mailmanager"
PREVIOUS_BINARY="$TARGET_DIR/mailmanager.previous"
CLI_BINARY="/usr/local/bin/mailmanager"
ENV_FILE="/etc/mailmanager/mailmanager.env"
UPDATER_ROOT="/var/lib/mailmanager-updater"
INSTALLED_VERSION_FILE="$UPDATER_ROOT/installed-version"

usage() {
    cat <<'EOF'
Usage: install-linux.sh [--version vX.Y.Z|latest] [--no-start]

Installs or upgrades MailManager from the official GitHub Release assets.
Existing application data, secrets, and unrelated environment settings are kept.
EOF
}

die() {
    echo "mailmanager installer: $*" >&2
    exit 1
}

is_stable_tag() {
    case "$1" in
        ''|*[!v0-9.]*) return 1 ;;
    esac
    printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || die "--version requires a value"
            VERSION=$2
            shift 2
            ;;
        --no-start)
            START_SERVICE=0
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "unknown argument: $1"
            ;;
    esac
done

[ "$(id -u)" -eq 0 ] || die "run this script as root"
for command in awk cat chmod chown curl dd getent grep groupadd id install ln mktemp mv rm sha256sum sleep systemctl uname useradd usermod; do
    command -v "$command" >/dev/null 2>&1 || die "required command not found: $command"
done
[ "$(uname -s)" = "Linux" ] || die "this installer only supports Linux"

case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "only Linux amd64 and arm64 are supported" ;;
esac

if [ "$VERSION" = "latest" ]; then
    LATEST_URL=$(curl --proto '=https' --tlsv1.2 --location --fail --silent --show-error \
        --retry 3 --retry-delay 2 --head --output /dev/null --write-out '%{url_effective}' \
        "https://github.com/$REPOSITORY/releases/latest")
    RELEASE_PAGE_PREFIX="https://github.com/$REPOSITORY/releases/tag/"
    case "$LATEST_URL" in
        "$RELEASE_PAGE_PREFIX"*) RELEASE_TAG=${LATEST_URL#"$RELEASE_PAGE_PREFIX"} ;;
        *) die "GitHub latest release redirected to an unexpected URL" ;;
    esac
else
    RELEASE_TAG=$VERSION
    case "$RELEASE_TAG" in
        v*) ;;
        *) RELEASE_TAG="v$RELEASE_TAG" ;;
    esac
fi
is_stable_tag "$RELEASE_TAG" || die "version must be latest or vX.Y.Z without leading zeroes"
VERSION=${RELEASE_TAG#v}
RELEASE_URL="https://github.com/$REPOSITORY/releases/download/$RELEASE_TAG"

TMP_DIR=$(mktemp -d)
TARGET_TEMP=""
cleanup() {
    rm -rf -- "$TMP_DIR"
    if [ -n "$TARGET_TEMP" ]; then
        rm -f -- "$TARGET_TEMP"
    fi
}
trap cleanup EXIT HUP INT TERM

download() {
    name=$1
    curl --proto '=https' --tlsv1.2 --location --fail --silent --show-error \
        --retry 3 --retry-delay 2 \
        --output "$TMP_DIR/$name" "$RELEASE_URL/$name"
}

BINARY_ASSET="mailmanager-linux-$ARCH"
ASSETS="$BINARY_ASSET mailmanager.service mailmanager-updater.service mailmanager-updater.path mailmanager.env.example"
download checksums.txt
for asset in $ASSETS; do
    download "$asset"
done

for asset in $ASSETS; do
    expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$TMP_DIR/checksums.txt")
    case "$expected" in
        ''|*[!0-9a-fA-F]*) die "missing or invalid checksum for $asset" ;;
    esac
    [ "${#expected}" -eq 64 ] || die "invalid checksum length for $asset"
    actual=$(sha256sum "$TMP_DIR/$asset" | awk '{ print $1 }')
    [ "$actual" = "$expected" ] || die "checksum verification failed for $asset"
done

if ! getent group mailmanager >/dev/null 2>&1; then
    groupadd --system mailmanager
fi
if ! id mailmanager >/dev/null 2>&1; then
    NOLOGIN=/usr/sbin/nologin
    [ -x "$NOLOGIN" ] || NOLOGIN=/sbin/nologin
    [ -x "$NOLOGIN" ] || NOLOGIN=/bin/false
    useradd --system --gid mailmanager --home-dir /var/lib/mailmanager --shell "$NOLOGIN" mailmanager
else
    usermod --append --groups mailmanager mailmanager
fi

install -d -m 0755 -o root -g root "$TARGET_DIR"
install -d -m 0700 -o mailmanager -g mailmanager /var/lib/mailmanager
install -d -m 0750 -o root -g mailmanager /etc/mailmanager
install -d -m 2750 -o root -g mailmanager "$UPDATER_ROOT"
install -d -m 2770 -o root -g mailmanager "$UPDATER_ROOT/inbox"
install -d -m 0700 -o root -g root "$UPDATER_ROOT/work" "$UPDATER_ROOT/backups"

if [ ! -f "$ENV_FILE" ]; then
    install -m 0640 -o root -g mailmanager "$TMP_DIR/mailmanager.env.example" "$ENV_FILE"
else
    chown root:mailmanager "$ENV_FILE"
    chmod 0640 "$ENV_FILE"
fi

set_environment() {
    key=$1
    value=$2
    temporary="$ENV_FILE.tmp.$$"
    awk -v key="$key" -v value="$value" '
        BEGIN { found = 0 }
        $0 ~ ("^" key "=") {
            if (!found) print key "=" value
            found = 1
            next
        }
        { print }
        END { if (!found) print key "=" value }
    ' "$ENV_FILE" > "$temporary"
    install -m 0640 -o root -g mailmanager "$temporary" "$ENV_FILE"
    rm -f -- "$temporary"
}

ensure_environment() {
    key=$1
    value=$2
    if ! grep -Eq "^${key}=" "$ENV_FILE"; then
        temporary="$ENV_FILE.tmp.$$"
        awk -v key="$key" -v value="$value" '{ print } END { print key "=" value }' \
            "$ENV_FILE" > "$temporary"
        install -m 0640 -o root -g mailmanager "$temporary" "$ENV_FILE"
        rm -f -- "$temporary"
    fi
}

read_environment() {
    awk -v key="$1" '$0 ~ ("^" key "=") { value = substr($0, index($0, "=") + 1) } END { print value }' "$ENV_FILE"
}

strip_environment_quotes() {
    value=$1
    case "$value" in
        \"*\") value=${value#\"}; value=${value%\"} ;;
        \'*\') value=${value#\'}; value=${value%\'} ;;
    esac
    printf '%s\n' "$value"
}

READY_URL=$(strip_environment_quotes "$(read_environment MAILMANAGER_UPDATE_READY_URL)")
if [ -z "$READY_URL" ]; then
    APP_ADDRESS=$(strip_environment_quotes "$(read_environment MAILMANAGER_ADDR)")
    [ -n "$APP_ADDRESS" ] || APP_ADDRESS=127.0.0.1:8080
    case "$APP_ADDRESS" in
        :*) READY_ADDRESS="127.0.0.1$APP_ADDRESS" ;;
        0.0.0.0:*) READY_ADDRESS="127.0.0.1:${APP_ADDRESS#0.0.0.0:}" ;;
        "[::]:"*) READY_ADDRESS="[::1]:${APP_ADDRESS#"[::]:"}" ;;
        *) READY_ADDRESS=$APP_ADDRESS ;;
    esac
    READY_URL="http://$READY_ADDRESS/readyz"
    set_environment MAILMANAGER_UPDATE_READY_URL "$READY_URL"
fi

ensure_environment MAILMANAGER_AUTO_UPDATE_ENABLED true
set_environment MAILMANAGER_UPDATE_INBOX_DIR "$UPDATER_ROOT/inbox"
set_environment MAILMANAGER_UPDATE_STATE_FILE "$UPDATER_ROOT/status.json"
set_environment MAILMANAGER_UPDATE_WORK_DIR "$UPDATER_ROOT/work"
set_environment MAILMANAGER_UPDATE_BACKUP_DIR "$UPDATER_ROOT/backups"
set_environment MAILMANAGER_UPDATE_BINARY_PATH "$TARGET_BINARY"
set_environment MAILMANAGER_UPDATE_INSTALLED_VERSION_FILE "$INSTALLED_VERSION_FILE"
set_environment MAILMANAGER_UPDATE_SERVICE mailmanager.service

if [ -e /etc/mailmanager/master.key ] && [ ! -f /etc/mailmanager/master.key ]; then
    die "/etc/mailmanager/master.key must be a regular file"
fi
if [ ! -f /etc/mailmanager/master.key ]; then
    KEY_TEMP="$TMP_DIR/master.key"
    dd if=/dev/urandom of="$KEY_TEMP" bs=32 count=1 status=none
    install -m 0600 -o mailmanager -g mailmanager "$KEY_TEMP" /etc/mailmanager/master.key
fi
chown mailmanager:mailmanager /etc/mailmanager/master.key
chmod 0600 /etc/mailmanager/master.key

install -m 0644 -o root -g root "$TMP_DIR/mailmanager.service" /etc/systemd/system/mailmanager.service
install -m 0644 -o root -g root "$TMP_DIR/mailmanager-updater.service" /etc/systemd/system/mailmanager-updater.service
install -m 0644 -o root -g root "$TMP_DIR/mailmanager-updater.path" /etc/systemd/system/mailmanager-updater.path
systemctl daemon-reload
systemctl enable mailmanager.service mailmanager-updater.path

EXISTING_BINARY=""
if [ -f "$TARGET_BINARY" ]; then
    EXISTING_BINARY=$TARGET_BINARY
elif [ -f "$CLI_BINARY" ]; then
    EXISTING_BINARY=$CLI_BINARY
fi
HAD_PREVIOUS=0
if [ -n "$EXISTING_BINARY" ]; then
    PREVIOUS_TEMP="$TARGET_DIR/.mailmanager.previous.$$"
    install -m 0755 -o root -g root "$EXISTING_BINARY" "$PREVIOUS_TEMP"
    mv -f -- "$PREVIOUS_TEMP" "$PREVIOUS_BINARY"
    HAD_PREVIOUS=1
fi

TARGET_TEMP="$TARGET_DIR/.mailmanager.install.$$"
install -m 0755 -o root -g root "$TMP_DIR/$BINARY_ASSET" "$TARGET_TEMP"
mv -f -- "$TARGET_TEMP" "$TARGET_BINARY"
TARGET_TEMP=""
ln -sfn "$TARGET_BINARY" "$CLI_BINARY"

write_installed_version() {
    marker_temp="$UPDATER_ROOT/.installed-version.$$"
    if ! (
        printf '%s\n' "$1" > "$marker_temp" &&
        chown root:mailmanager "$marker_temp" &&
        chmod 0640 "$marker_temp" &&
        mv -f -- "$marker_temp" "$INSTALLED_VERSION_FILE"
    ); then
        rm -f -- "$marker_temp"
        return 1
    fi
}

wait_for_ready() {
    attempt=0
    while [ "$attempt" -lt 60 ]; do
        if curl --proto '=http,https' --fail --silent --output /dev/null --max-time 1 "$READY_URL"; then
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done
    return 1
}

rollback_and_fail() {
    reason=$1
    if [ "$HAD_PREVIOUS" -ne 1 ]; then
        die "$reason; no previous binary is available for rollback"
    fi
    rollback_temp="$TARGET_DIR/.mailmanager.rollback.$$"
    install -m 0755 -o root -g root "$PREVIOUS_BINARY" "$rollback_temp"
    mv -f -- "$rollback_temp" "$TARGET_BINARY"
    if systemctl restart mailmanager.service && wait_for_ready; then
        die "$reason; the previous binary was restored successfully"
    fi
    die "$reason; restoring the previous binary did not recover the service"
}

if [ "$START_SERVICE" -eq 1 ]; then
    if ! systemctl restart mailmanager.service; then
        rollback_and_fail "MailManager $VERSION failed to restart"
    fi
    if ! wait_for_ready; then
        rollback_and_fail "MailManager $VERSION did not become ready"
    fi
    if ! write_installed_version "$VERSION"; then
        rollback_and_fail "MailManager $VERSION became ready but its installed-version marker could not be recorded"
    fi
    systemctl restart mailmanager-updater.path
    echo "Installed MailManager $VERSION and verified $READY_URL"
else
    write_installed_version "$VERSION" || die "could not record the staged installed-version marker"
    echo "Installed MailManager $VERSION without starting or health-checking it."
    if [ "$HAD_PREVIOUS" -eq 1 ]; then
        echo "The previous binary is preserved at $PREVIOUS_BINARY."
    fi
    echo "Review $ENV_FILE, then run: systemctl restart mailmanager.service mailmanager-updater.path"
fi
