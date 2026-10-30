#!/usr/bin/env bash
# install-service.sh - cài scs-server thành dịch vụ systemd (tự chạy khi khởi động lại máy).
#
# Cách dùng (cần sudo):
#   sudo bash scripts/install-service.sh              cài hoặc cập nhật binary rồi khởi động lại dịch vụ
#   sudo bash scripts/install-service.sh --uninstall  gỡ dịch vụ (GIỮ cấu hình và dữ liệu)
#
# Script làm:
#   - tạo user hệ thống scs (không đăng nhập được)
#   - chép bin/scs-server → /opt/scs-server/ (chưa có thì build bằng go)
#   - lần đầu: chép server/.env → /etc/scs-server/server.env (scs, 600); đã có thì GIỮ NGUYÊN
#   - cài deploy/systemd/scs-server.service, bật tự chạy khi khởi động, khởi động ngay
#   - cài lệnh scs-login-code để lấy mã đăng nhập web
# Chạy lại nhiều lần được.

set -Eeuo pipefail

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UNIT_SRC="$SERVER_DIR/deploy/systemd/scs-server.service"
BIN_SRC="$SERVER_DIR/bin/scs-server"
SVC_USER=scs
OPT_DIR=/opt/scs-server
ETC_DIR=/etc/scs-server
ENV_FILE=$ETC_DIR/server.env
DATA_DIR=/var/lib/scs-server
UNIT_DST=/etc/systemd/system/scs-server.service
LOGIN_CMD=/usr/local/bin/scs-login-code

if [[ -t 1 ]]; then
	C_RED=$'\e[31m' C_GRN=$'\e[32m' C_YEL=$'\e[33m' C_0=$'\e[0m'
else
	C_RED='' C_GRN='' C_YEL='' C_0=''
fi
ok()   { printf '  %s✔%s %s\n' "$C_GRN" "$C_0" "$*"; }
warn() { printf '  %s!%s %s\n' "$C_YEL" "$C_0" "$*"; }
die() {
	printf '%s✖ LỖI: %s%s\n' "$C_RED" "$1" "$C_0" >&2
	[[ $# -gt 1 ]] && printf '  Cách xử lý: %s\n' "$2" >&2
	exit 1
}
trap 'die "Lệnh ở dòng $LINENO thất bại" "Xem thông báo phía trên"' ERR

# env_value đọc một biến trong file .env (không source file, không chạy gì trong đó).
env_value() {
	local line
	line="$(grep -E "^(export[[:space:]]+)?$2=" "$1" | tail -n1 || true)"
	line="${line#*=}"
	line="$(sed -E 's/^[[:space:]]*//; s/^#.*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//' <<<"$line")"
	line="${line#\"}"; line="${line%\"}"; line="${line#\'}"; line="${line%\'}"
	printf '%s' "$line"
}

[[ $EUID -eq 0 ]] || die "Cần chạy bằng root" "sudo bash $0 $*"
command -v systemctl >/dev/null || die "Máy không dùng systemd" "Script chỉ hỗ trợ Ubuntu có systemd"

if [[ "${1:-}" == "--uninstall" ]]; then
	systemctl disable --now scs-server 2>/dev/null || true
	rm -f "$UNIT_DST" "$LOGIN_CMD"
	systemctl daemon-reload
	ok "Đã gỡ dịch vụ scs-server. Giữ lại: $ETC_DIR (cấu hình), $DATA_DIR (dữ liệu), $OPT_DIR (binary), user $SVC_USER"
	exit 0
fi
[[ $# -eq 0 ]] || die "Tham số không hợp lệ: $*" "Chỉ có --uninstall"

# ---- binary
if [[ ! -x "$BIN_SRC" ]]; then
	GO="$(command -v go || echo /usr/local/go/bin/go)"
	[[ -x "$GO" ]] || die "Chưa có $BIN_SRC và không có go để build" "Chạy scripts/setup.sh trước, hoặc build trên máy khác rồi chép vào bin/"
	echo "Build scs-server..."
	(cd "$SERVER_DIR" && CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w" -o "$BIN_SRC" ./cmd/scs-server)
fi
"$BIN_SRC" version >/dev/null || die "$BIN_SRC không chạy được trên máy này" "Build lại cho đúng kiến trúc (GOARCH)"

# ---- user, thư mục
if ! id "$SVC_USER" >/dev/null 2>&1; then
	useradd --system --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
	ok "Đã tạo user hệ thống $SVC_USER"
fi
install -d -o root -g root -m 755 "$OPT_DIR" "$ETC_DIR"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 700 "$DATA_DIR"
install -o root -g root -m 755 "$BIN_SRC" "$OPT_DIR/scs-server"
[[ -f "$SERVER_DIR/README.md" ]] && install -o root -g root -m 644 "$SERVER_DIR/README.md" "$OPT_DIR/README.md"
ok "Binary: $OPT_DIR/scs-server ($("$OPT_DIR/scs-server" version))"

# ---- cấu hình
if [[ ! -f "$ENV_FILE" ]]; then
	[[ -f "$SERVER_DIR/.env" ]] || die "Chưa có $SERVER_DIR/.env" "Chạy scripts/setup.sh để tạo .env, hoặc tự tạo $ENV_FILE"
	install -o "$SVC_USER" -g "$SVC_USER" -m 600 "$SERVER_DIR/.env" "$ENV_FILE"
	ok "Đã chép cấu hình → $ENV_FILE (từ nay sửa file này, không phải server/.env)"
else
	chown "$SVC_USER:$SVC_USER" "$ENV_FILE"
	chmod 600 "$ENV_FILE"
	ok "Giữ nguyên cấu hình đang có: $ENV_FILE"
fi
[[ "$(env_value "$ENV_FILE" SCS_ENV)" == prod ]] ||
	warn "SCS_ENV trong $ENV_FILE chưa phải prod (đang chạy thử thì bỏ qua)"
for key in SCS_TLS_CERT SCS_TLS_KEY SCS_TLS_CLIENT_CA SCS_STORAGE_ROOT; do
	val="$(env_value "$ENV_FILE" "$key")"
	if [[ -n "$val" && "$val" != /* ]]; then
		warn "$key=$val là đường dẫn tương đối, sẽ tính từ $DATA_DIR; nên dùng đường dẫn tuyệt đối"
	fi
	if [[ "$key" == SCS_TLS_* && -n "$val" && "$val" == /* ]] && ! sudo -u "$SVC_USER" test -r "$val"; then
		warn "user $SVC_USER không đọc được $val ($key): dịch vụ sẽ không khởi động được"
	fi
done

# ---- lệnh lấy mã đăng nhập
cat >"$LOGIN_CMD" <<EOF
#!/bin/sh
# Lấy mã đăng nhập web quản trị của dịch vụ scs-server (chạy bằng user $SVC_USER).
# Thêm -watch để tự cập nhật, -unlock để mở khóa khi bị khóa do nhập sai nhiều.
exec sudo -u $SVC_USER env SCS_CONTROL_SOCKET=/run/scs-server/control.sock \\
	$OPT_DIR/scs-server login-code -env-file $ENV_FILE "\$@"
EOF
chmod 755 "$LOGIN_CMD"
ok "Lệnh lấy mã đăng nhập: scs-login-code"

# ---- dịch vụ
install -o root -g root -m 644 "$UNIT_SRC" "$UNIT_DST"
systemctl daemon-reload
systemctl enable scs-server >/dev/null
systemctl restart scs-server
ok "Đã cài $UNIT_DST, bật tự chạy khi khởi động máy"

sleep 3
if systemctl is-active --quiet scs-server; then
	ok "scs-server đang chạy"
else
	journalctl -u scs-server -n 30 --no-pager >&2 || true
	die "scs-server không chạy được" "Xem log phía trên; sửa $ENV_FILE rồi: sudo systemctl restart scs-server"
fi
cat <<EOF

  Xem log:          journalctl -u scs-server -f
  Trạng thái:       systemctl status scs-server
  Lấy mã đăng nhập: scs-login-code
  Sửa cấu hình:     sudo nano $ENV_FILE && sudo systemctl restart scs-server
  Cập nhật binary:  build lại bin/scs-server rồi chạy lại script này
EOF
