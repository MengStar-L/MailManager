#!/usr/bin/env sh
set -eu

REPOSITORY="MengStar-L/MailManager"
DEFAULT_INSTALL_DIR="/opt/mailmanager"
VERSION=${MAILMANAGER_VERSION:-latest}
INSTALL_DIR=${MAILMANAGER_INSTALL_DIR:-}
PUBLIC_URL=${MAILMANAGER_PUBLIC_URL:-}
START_SERVICE=1
ASSUME_YES=0
DRY_RUN=0
CLI_BINARY="/usr/local/bin/mailmanager"

usage() {
    cat <<'EOF'
Usage: install-linux.sh [options]

Installs or upgrades MailManager from the official GitHub Release assets.

Options:
  --install-dir PATH   Installation root (default: /opt/mailmanager)
  --public-url URL     Public HTTPS origin, for example https://mail.example.com
  --version VERSION    Stable version tag or latest (default: latest)
  --no-start           Install without starting or health-checking the service
  --yes                Skip the confirmation prompt
  --dry-run            Validate input and print the resulting layout without changes
  -h, --help           Show this help
EOF
}

die() {
    echo "mailmanager installer: $*" >&2
    exit 1
}

has_tty() {
    [ -t 0 ] && [ -r /dev/tty ] && [ -w /dev/tty ]
}

prompt() {
    label=$1
    default_value=$2
    if [ -n "$default_value" ]; then
        printf '%s [%s]: ' "$label" "$default_value" > /dev/tty
    else
        printf '%s: ' "$label" > /dev/tty
    fi
    IFS= read -r answer < /dev/tty || die "could not read interactive input"
    if [ -z "$answer" ]; then
        answer=$default_value
    fi
    printf '%s\n' "$answer"
}

confirm_install() {
    printf '确认按以上配置安装？[Y/n]: ' > /dev/tty
    IFS= read -r answer < /dev/tty || die "could not read confirmation"
    case "$answer" in
        ''|y|Y|yes|YES) ;;
        *) die "installation cancelled" ;;
    esac
}

is_stable_tag() {
    value=$1
    case "$value" in
        v*) value=${value#v} ;;
    esac
    old_ifs=$IFS
    IFS=.
    # Splitting semantic-version components on dots is intentional.
    # shellcheck disable=SC2086
    set -- $value
    IFS=$old_ifs
    [ "$#" -eq 3 ] || return 1
    for part in "$@"; do
        case "$part" in
            ''|*[!0-9]*) return 1 ;;
            0) ;;
            0*) return 1 ;;
        esac
    done
}

validate_install_dir() {
    value=$1
    case "$value" in
        /*) ;;
        *) die "--install-dir must be an absolute path" ;;
    esac
    case "$value" in
        *..*) die "--install-dir must not contain '..'" ;;
        *//*|*/./*|*/.) die "--install-dir must use a normalized path" ;;
    esac
    case "$value" in
        *[!A-Za-z0-9._/-]*) die "--install-dir may contain only letters, numbers, '.', '_', '-' and '/'" ;;
    esac
    case "$value" in
        /|/bin|/bin/*|/boot|/boot/*|/dev|/dev/*|/etc|/etc/*|/home|/home/*|/lib|/lib/*|/lib64|/lib64/*|/opt|/proc|/proc/*|/root|/root/*|/run|/run/*|/sbin|/sbin/*|/sys|/sys/*|/tmp|/tmp/*|/usr|/usr/*|/var)
            die "--install-dir points to a protected system path"
            ;;
    esac
}

validate_public_url() {
    value=$1
    case "$value" in
        https://*) ;;
        *) die "--public-url must be an HTTPS origin such as https://mail.example.com" ;;
    esac
    authority=${value#https://}
    case "$authority" in
        ''|*/*|*\?*|*\#*|*@*|*\\*|*[!A-Za-z0-9.:-]*)
            die "--public-url must contain only a domain and optional port"
            ;;
        *:*:*) die "--public-url contains an invalid port" ;;
    esac
    host=${authority%%:*}
    [ "${#host}" -le 253 ] || die "--public-url domain is too long"
    case "$host" in
        .*|*.|-*|*-) die "--public-url contains an invalid domain" ;;
        *.*) ;;
        *) die "--public-url must contain a fully qualified domain name" ;;
    esac
    old_ifs=$IFS
    IFS=.
    # Splitting domain labels on dots is intentional.
    # shellcheck disable=SC2086
    set -- $host
    IFS=$old_ifs
    for label in "$@"; do
        if [ -z "$label" ] || [ "${#label}" -gt 63 ]; then
            die "--public-url contains an invalid domain label"
        fi
        case "$label" in
            -*|*-|*[!A-Za-z0-9-]*) die "--public-url contains an invalid domain label" ;;
        esac
    done
    case "$authority" in
        *:*)
            port=${authority##*:}
            case "$port" in
                ''|*[!0-9]*) die "--public-url contains an invalid port" ;;
            esac
            if [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
                die "--public-url port must be between 1 and 65535"
            fi
            ;;
    esac
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --install-dir)
            [ "$#" -ge 2 ] || die "--install-dir requires a value"
            INSTALL_DIR=$2
            shift 2
            ;;
        --public-url)
            [ "$#" -ge 2 ] || die "--public-url requires a value"
            PUBLIC_URL=$2
            shift 2
            ;;
        --version)
            [ "$#" -ge 2 ] || die "--version requires a value"
            VERSION=$2
            shift 2
            ;;
        --no-start)
            START_SERVICE=0
            shift
            ;;
        --yes)
            ASSUME_YES=1
            shift
            ;;
        --dry-run)
            DRY_RUN=1
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

if [ -z "$INSTALL_DIR" ]; then
    if has_tty; then
        INSTALL_DIR=$(prompt "安装目录" "$DEFAULT_INSTALL_DIR")
    else
        INSTALL_DIR=$DEFAULT_INSTALL_DIR
    fi
fi
while [ "$INSTALL_DIR" != "/" ] && [ "${INSTALL_DIR%/}" != "$INSTALL_DIR" ]; do
    INSTALL_DIR=${INSTALL_DIR%/}
done
validate_install_dir "$INSTALL_DIR"

if [ -z "$PUBLIC_URL" ]; then
    if has_tty; then
        PUBLIC_URL=$(prompt "公开 HTTPS 地址" "")
    else
        die "--public-url is required without an interactive terminal"
    fi
fi
while [ "${PUBLIC_URL%/}" != "$PUBLIC_URL" ]; do
    PUBLIC_URL=${PUBLIC_URL%/}
done
validate_public_url "$PUBLIC_URL"
if [ "$VERSION" != "latest" ]; then
    is_stable_tag "$VERSION" || die "version must be latest or vX.Y.Z without leading zeroes"
fi

PUBLIC_AUTHORITY=${PUBLIC_URL#https://}
PUBLIC_HOST=${PUBLIC_AUTHORITY%%:*}
PUBLIC_PORT=443
case "$PUBLIC_AUTHORITY" in
    *:*) PUBLIC_PORT=${PUBLIC_AUTHORITY##*:} ;;
esac
TARGET_DIR="$INSTALL_DIR/bin"
TARGET_BINARY="$TARGET_DIR/mailmanager"
PREVIOUS_BINARY="$TARGET_DIR/mailmanager.previous"
CONFIG_DIR="$INSTALL_DIR/config"
ENV_FILE="$CONFIG_DIR/mailmanager.env"
MASTER_KEY_FILE="$CONFIG_DIR/master.key"
DATA_DIR="$INSTALL_DIR/data"
UPDATER_ROOT="$INSTALL_DIR/updater"
INSTALLED_VERSION_FILE="$UPDATER_ROOT/installed-version"
READY_URL="http://127.0.0.1:8080/readyz"

show_summary() {
    if [ "$START_SERVICE" -eq 1 ]; then
        start_label=yes
    else
        start_label=no
    fi
    printf '\nMailManager 安装摘要\n'
    printf '  版本:       %s\n' "$VERSION"
    printf '  安装目录:   %s\n' "$INSTALL_DIR"
    printf '  公开地址:   %s\n' "$PUBLIC_URL"
    printf '  本机监听:   127.0.0.1:8080\n'
    printf '  配置文件:   %s\n' "$ENV_FILE"
    printf '  数据目录:   %s\n' "$DATA_DIR"
    printf '  更新目录:   %s\n' "$UPDATER_ROOT"
    printf '  立即启动:   %s\n' "$start_label"
}

show_summary
if [ "$DRY_RUN" -eq 1 ]; then
    echo "预览完成，没有下载或修改任何文件。"
    exit 0
fi

if [ "$ASSUME_YES" -ne 1 ]; then
    has_tty || die "--yes is required without an interactive terminal"
    confirm_install
fi

[ "$(id -u)" -eq 0 ] || die "run this script as root"
[ ! -L "$INSTALL_DIR" ] || die "installation root must not be a symbolic link"
for command in awk cat chmod chown curl dd getent grep groupadd id install ln mktemp mv realpath rm sed sha256sum sleep systemctl uname useradd usermod; do
    command -v "$command" >/dev/null 2>&1 || die "required command not found: $command"
done
[ "$(uname -s)" = "Linux" ] || die "this installer only supports Linux"
RESOLVED_INSTALL_DIR=$(realpath -m -- "$INSTALL_DIR")
[ "$RESOLVED_INSTALL_DIR" = "$INSTALL_DIR" ] || die "--install-dir must be normalized and must not traverse symbolic links"

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

render_root_template() {
    sed "s|/opt/mailmanager|$INSTALL_DIR|g" "$1" > "$2"
}

BINARY_ASSET="mailmanager-linux-$ARCH"
ASSETS="$BINARY_ASSET mailmanager.service mailmanager-updater.service mailmanager-updater.path mailmanager.env.example nginx.conf.example"
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
    useradd --system --gid mailmanager --home-dir "$DATA_DIR" --shell "$NOLOGIN" mailmanager
else
    usermod --gid mailmanager --home "$DATA_DIR" mailmanager
fi

install -d -m 0755 -o root -g root "$INSTALL_DIR" "$TARGET_DIR"
install -d -m 0750 -o root -g mailmanager "$CONFIG_DIR"
install -d -m 0700 -o mailmanager -g mailmanager "$DATA_DIR" "$DATA_DIR/attachments" "$DATA_DIR/draft-blobs"
install -d -m 2750 -o root -g mailmanager "$UPDATER_ROOT"
install -d -m 2770 -o root -g mailmanager "$UPDATER_ROOT/inbox"
install -d -m 0700 -o root -g root "$UPDATER_ROOT/work" "$UPDATER_ROOT/backups"

if [ ! -f "$ENV_FILE" ]; then
    render_root_template "$TMP_DIR/mailmanager.env.example" "$TMP_DIR/mailmanager.env"
    install -m 0640 -o root -g mailmanager "$TMP_DIR/mailmanager.env" "$ENV_FILE"
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

set_environment MAILMANAGER_ADDR 127.0.0.1:8080
set_environment MAILMANAGER_PUBLIC_URL "$PUBLIC_URL"
set_environment MAILMANAGER_DATA_DIR "$DATA_DIR"
set_environment MAILMANAGER_DATABASE_PATH "$DATA_DIR/mailmanager.db"
set_environment MAILMANAGER_MASTER_KEY_FILE "$MASTER_KEY_FILE"
set_environment MAILMANAGER_ATTACHMENT_CACHE_DIR "$DATA_DIR/attachments"
ensure_environment MAILMANAGER_AUTO_UPDATE_ENABLED true
set_environment MAILMANAGER_UPDATE_INBOX_DIR "$UPDATER_ROOT/inbox"
set_environment MAILMANAGER_UPDATE_STATE_FILE "$UPDATER_ROOT/status.json"
set_environment MAILMANAGER_UPDATE_WORK_DIR "$UPDATER_ROOT/work"
set_environment MAILMANAGER_UPDATE_BACKUP_DIR "$UPDATER_ROOT/backups"
set_environment MAILMANAGER_UPDATE_BINARY_PATH "$TARGET_BINARY"
set_environment MAILMANAGER_UPDATE_INSTALLED_VERSION_FILE "$INSTALLED_VERSION_FILE"
set_environment MAILMANAGER_UPDATE_READY_URL "$READY_URL"
set_environment MAILMANAGER_UPDATE_SERVICE mailmanager.service

if [ -e "$MASTER_KEY_FILE" ] && [ ! -f "$MASTER_KEY_FILE" ]; then
    die "$MASTER_KEY_FILE must be a regular file"
fi
if [ ! -f "$MASTER_KEY_FILE" ]; then
    KEY_TEMP="$TMP_DIR/master.key"
    dd if=/dev/urandom of="$KEY_TEMP" bs=32 count=1 status=none
    install -m 0600 -o mailmanager -g mailmanager "$KEY_TEMP" "$MASTER_KEY_FILE"
fi
chown mailmanager:mailmanager "$MASTER_KEY_FILE"
chmod 0600 "$MASTER_KEY_FILE"

sed \
    -e "s|mail.example.com|$PUBLIC_HOST|g" \
    -e "s|listen 443 ssl|listen $PUBLIC_PORT ssl|g" \
    -e "s|listen \[::\]:443 ssl|listen [::]:$PUBLIC_PORT ssl|g" \
    "$TMP_DIR/nginx.conf.example" > "$TMP_DIR/nginx.conf"
install -m 0640 -o root -g mailmanager "$TMP_DIR/nginx.conf" "$CONFIG_DIR/nginx.conf.example"

render_root_template "$TMP_DIR/mailmanager.service" "$TMP_DIR/mailmanager-rendered.service"
render_root_template "$TMP_DIR/mailmanager-updater.service" "$TMP_DIR/mailmanager-updater-rendered.service"
render_root_template "$TMP_DIR/mailmanager-updater.path" "$TMP_DIR/mailmanager-updater-rendered.path"
install -m 0644 -o root -g root "$TMP_DIR/mailmanager-rendered.service" /etc/systemd/system/mailmanager.service
install -m 0644 -o root -g root "$TMP_DIR/mailmanager-updater-rendered.service" /etc/systemd/system/mailmanager-updater.service
install -m 0644 -o root -g root "$TMP_DIR/mailmanager-updater-rendered.path" /etc/systemd/system/mailmanager-updater.path

HAD_PREVIOUS=0
if [ -f "$TARGET_BINARY" ]; then
    PREVIOUS_TEMP="$TARGET_DIR/.mailmanager.previous.$$"
    install -m 0755 -o root -g root "$TARGET_BINARY" "$PREVIOUS_TEMP"
    mv -f -- "$PREVIOUS_TEMP" "$PREVIOUS_BINARY"
    HAD_PREVIOUS=1
fi

TARGET_TEMP="$TARGET_DIR/.mailmanager.install.$$"
install -m 0755 -o root -g root "$TMP_DIR/$BINARY_ASSET" "$TARGET_TEMP"
mv -f -- "$TARGET_TEMP" "$TARGET_BINARY"
TARGET_TEMP=""
ln -sfn "$TARGET_BINARY" "$CLI_BINARY"

if command -v systemd-analyze >/dev/null 2>&1; then
    systemd-analyze verify /etc/systemd/system/mailmanager.service \
        /etc/systemd/system/mailmanager-updater.service \
        /etc/systemd/system/mailmanager-updater.path
fi
systemctl daemon-reload
systemctl enable mailmanager.service mailmanager-updater.path

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

cat <<EOF

后续步骤
  1. 使用 $CONFIG_DIR/nginx.conf.example 配置 Nginx。
  2. 确认 $PUBLIC_URL 已使用有效 TLS 证书提供服务。
  3. 读取初始化令牌：sudo cat $DATA_DIR/bootstrap-token
  4. 打开 $PUBLIC_URL 完成管理员初始化。
EOF
