#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
INSTALLER="$ROOT/scripts/install-linux.sh"
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT HUP INT TERM

fail() {
    echo "install-linux test: $*" >&2
    exit 1
}

expect_failure() {
    name=$1
    shift
    if sh "$INSTALLER" "$@" >"$TMP_DIR/$name.out" 2>"$TMP_DIR/$name.err"; then
        fail "$name unexpectedly succeeded"
    fi
}

default_output=$(sh "$INSTALLER" --dry-run --public-url https://mail.example.com --yes)
printf '%s\n' "$default_output" | grep -F "安装目录:   /opt/mailmanager" >/dev/null || fail "default root missing"
printf '%s\n' "$default_output" | grep -F "0.0.0.0:8080" >/dev/null || fail "default listening address missing"
printf '%s\n' "$default_output" | grep -F "立即启动:   yes" >/dev/null || fail "default start state missing"

custom_root="/srv/mailmanager-test-$$"
custom_output=$(sh "$INSTALLER" --dry-run --install-dir "$custom_root" --port 19090 --public-url https://mail.example.com:8443 --no-start --yes)
printf '%s\n' "$custom_output" | grep -F "配置文件:   $custom_root/config/mailmanager.env" >/dev/null || fail "custom config path missing"
printf '%s\n' "$custom_output" | grep -F "更新目录:   $custom_root/updater" >/dev/null || fail "custom updater path missing"
printf '%s\n' "$custom_output" | grep -F "0.0.0.0:19090" >/dev/null || fail "custom listening address missing"
printf '%s\n' "$custom_output" | grep -F "立即启动:   no" >/dev/null || fail "no-start state missing"
[ ! -e "$custom_root" ] || fail "dry-run created the installation directory"

expect_failure relative-root --dry-run --install-dir ../mailmanager --public-url https://mail.example.com --yes
expect_failure protected-root --dry-run --install-dir /etc/mailmanager --public-url https://mail.example.com --yes
expect_failure dotted-protected-root --dry-run --install-dir /etc/./mailmanager --public-url https://mail.example.com --yes
expect_failure doubled-slash-root --dry-run --install-dir //etc/mailmanager --public-url https://mail.example.com --yes
expect_failure unsafe-root --dry-run --install-dir "/srv/mail manager" --public-url https://mail.example.com --yes
expect_failure insecure-url --dry-run --public-url http://mail.example.com --yes
expect_failure url-path --dry-run --public-url https://mail.example.com/app --yes
expect_failure invalid-public-port --dry-run --public-url https://mail.example.com:70000 --yes
expect_failure local-port-zero --dry-run --port 0 --public-url https://mail.example.com --yes
expect_failure local-port-out-of-range --dry-run --port 65536 --public-url https://mail.example.com --yes
expect_failure local-port-non-numeric --dry-run --port abc --public-url https://mail.example.com --yes
expect_failure missing-local-port --dry-run --public-url https://mail.example.com --yes --port
expect_failure invalid-version --dry-run --public-url https://mail.example.com --version v01.0.0 --yes
expect_failure missing-url --dry-run --yes

help_output=$(sh "$INSTALLER" --help)
for option in --install-dir --port --public-url --version --no-start --yes --dry-run; do
    printf '%s\n' "$help_output" | grep -F -- "$option" >/dev/null || fail "help is missing $option"
done

echo "install-linux tests passed"
