#!/usr/bin/env bash
# install.sh - cài SCS-Agent lên một máy Ubuntu tại điểm bán (dịch vụ systemd, user riêng).
#
# Cách dùng (cần sudo):
#   sudo bash scripts/install.sh --server https://<IP WAN server>:<cổng> --ca <ca.pem> [tùy chọn]
#   sudo bash scripts/install.sh --check --server ... --ca ...   chỉ kiểm tra, không cài gì
#   sudo bash scripts/install.sh --uninstall                     gỡ dịch vụ, GIỮ khóa máy và trạng thái
#   sudo bash scripts/install.sh --uninstall --purge             gỡ hết (máy phải đăng ký và duyệt lại)
#
# Tùy chọn:
#   --server URL        địa chỉ cổng agent của server, ví dụ https://203.0.113.10:16222
#   --ca FILE           CA của server (ca.pem tạo bằng scs-server pki init trên máy server)
#   --watch LIST        dịch vụ systemd cần báo trạng thái, ngăn bằng dấu phẩy (ví dụ pos.service)
#   --binary FILE       binary scs-agent (mặc định agent/bin/scs-agent; chưa có thì build nếu có go)
#   --yes               không hỏi xác nhận
#
# Script làm: kiểm điều kiện (Ubuntu, systemd, kiến trúc, hostname, đồng hồ, kết nối tới server),
# tạo user hệ thống scs-agent, chép binary vào /opt/scs-agent, CA và cấu hình vào /etc/scs-agent,
# cài dịch vụ scs-agent.service (giới hạn CPU/RAM, thu hẹp quyền), khởi động và in mã khóa máy
# để admin đối chiếu khi duyệt trên web. Chạy lại nhiều lần được.

# Chạy bằng sh (dash) thì tự chuyển sang bash: script dùng cú pháp của bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Eeuo pipefail

AGENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UNIT_SRC="$AGENT_DIR/deploy/systemd/scs-agent.service"
SVC_USER=scs-agent
OPT_DIR=/opt/scs-agent
ETC_DIR=/etc/scs-agent
DATA_DIR=/var/lib/scs-agent
UNIT_DST=/etc/systemd/system/scs-agent.service
LINK=/usr/local/bin/scs-agent

SERVER="" CA_FILE="" WATCH="" BIN_SRC="$AGENT_DIR/bin/scs-agent"
MODE=install PURGE=0 YES=0
WARNINGS=0 ERRORS=0

if [[ -t 1 ]]; then
	C_RED=$'\e[31m' C_GRN=$'\e[32m' C_YEL=$'\e[33m' C_B=$'\e[1m' C_0=$'\e[0m'
else
	C_RED='' C_GRN='' C_YEL='' C_B='' C_0=''
fi
step() { printf '\n%s==> %s%s\n' "$C_B" "$*" "$C_0"; }
ok()   { printf '  %s✔%s %s\n' "$C_GRN" "$C_0" "$*"; }
warn() { printf '  %s!%s %s\n' "$C_YEL" "$C_0" "$*"; WARNINGS=$((WARNINGS + 1)); }
err()  { printf '  %s✖%s %s\n' "$C_RED" "$C_0" "$*"; ERRORS=$((ERRORS + 1)); }
die() {
	printf '\n%s✖ LỖI: %s%s\n' "$C_RED$C_B" "$1" "$C_0" >&2
	[[ $# -gt 1 ]] && printf '  Cách xử lý: %s\n' "$2" >&2
	exit 1
}
trap 'die "Lệnh ở dòng $LINENO thất bại" "Xem thông báo phía trên"' ERR
usage() { sed -n '2,21p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
	case "$1" in
		--server) SERVER="${2:-}"; shift 2 ;;
		--ca) CA_FILE="${2:-}"; shift 2 ;;
		--watch) WATCH="${2:-}"; shift 2 ;;
		--binary) BIN_SRC="${2:-}"; shift 2 ;;
		--check) MODE=check; shift ;;
		--uninstall) MODE=uninstall; shift ;;
		--purge) PURGE=1; shift ;;
		--yes|-y) YES=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) die "Tham số không hợp lệ: $1" "Xem: bash $0 --help" ;;
	esac
done

[[ $EUID -eq 0 ]] || die "Cần chạy bằng root" "sudo bash $0 ..."
command -v systemctl >/dev/null || die "Máy không dùng systemd" "Chỉ hỗ trợ Ubuntu có systemd"

# ---------------------------------------------------------------- gỡ cài
if [[ $MODE == uninstall ]]; then
	step "Gỡ SCS-Agent"
	systemctl disable --now scs-agent 2>/dev/null || true
	rm -f "$UNIT_DST" "$LINK"
	systemctl daemon-reload
	ok "Đã dừng và gỡ dịch vụ scs-agent"
	if [[ $PURGE -eq 1 ]]; then
		if [[ $YES -eq 0 ]]; then
			read -r -p "  Xóa luôn khóa máy, chứng chỉ, trạng thái và user $SVC_USER? Máy sẽ phải đăng ký và duyệt lại. [y/N] " a
			[[ "$a" =~ ^[yY]$ ]] || { echo "  Giữ lại dữ liệu."; exit 0; }
		fi
		rm -rf "$ETC_DIR" "$DATA_DIR" "$OPT_DIR"
		id "$SVC_USER" >/dev/null 2>&1 && userdel "$SVC_USER"
		ok "Đã xóa $ETC_DIR, $DATA_DIR, $OPT_DIR và user $SVC_USER"
	else
		ok "Giữ lại $ETC_DIR (khóa máy, chứng chỉ), $DATA_DIR, $OPT_DIR; cài lại thì không phải duyệt lại"
	fi
	exit 0
fi

# ---------------------------------------------------------------- 1. kiểm tra
step "1. Kiểm tra điều kiện"

# Hệ điều hành
if [[ -r /etc/os-release ]] && grep -q '^ID=ubuntu' /etc/os-release; then
	ok "$(sed -n 's/^PRETTY_NAME=//p' /etc/os-release | tr -d '"')"
else
	warn "Không phải Ubuntu: agent được viết và thử cho Ubuntu"
fi

# Tham số bắt buộc khi cài lần đầu (cài lại thì dùng cấu hình đang có)
if [[ -z "$SERVER" && -r "$ETC_DIR/agent.env" ]]; then
	SERVER="$(sed -n 's/^SCS_SERVER=//p' "$ETC_DIR/agent.env" | tail -n1)"
	[[ -n "$SERVER" ]] && ok "Dùng địa chỉ server đang cấu hình: $SERVER"
fi
if [[ -z "$CA_FILE" && -r "$ETC_DIR/server-ca.pem" ]]; then
	CA_FILE="$ETC_DIR/server-ca.pem"
	ok "Dùng CA đang cài: $CA_FILE"
fi
[[ -n "$SERVER" ]] || die "Thiếu --server" "Ví dụ: --server https://203.0.113.10:16222 (IP WAN của server : cổng chuyển tiếp)"
[[ "$SERVER" =~ ^https://[^/:@[:space:]]+:[0-9]{1,5}/?$ ]] ||
	die "--server phải có dạng https://<IP>:<cổng>, đang là \"$SERVER\""
SERVER="${SERVER%/}"
[[ -n "$CA_FILE" ]] || die "Thiếu --ca" "Lấy file ca.pem trên máy server (tạo bằng scs-server pki init) rồi chép sang máy này"
[[ -r "$CA_FILE" ]] || die "Không đọc được $CA_FILE"
if command -v openssl >/dev/null; then
	if subject="$(openssl x509 -in "$CA_FILE" -noout -subject 2>/dev/null)"; then
		ok "CA của server: ${subject#subject=}"
	else
		die "$CA_FILE không phải chứng chỉ PEM hợp lệ"
	fi
else
	grep -q "BEGIN CERTIFICATE" "$CA_FILE" || die "$CA_FILE không phải chứng chỉ PEM"
	warn "Không có openssl, chỉ kiểm sơ bộ file CA"
fi
for s in ${WATCH//,/ }; do
	[[ "$s" =~ ^[A-Za-z0-9@_.:][A-Za-z0-9@_.:-]*$ ]] || die "--watch: \"$s\" không phải tên dịch vụ systemd hợp lệ"
done

# Binary
if [[ ! -x "$BIN_SRC" ]]; then
	GO="$(command -v go || echo /usr/local/go/bin/go)"
	if [[ $MODE == install && -x "$GO" ]]; then
		echo "  Chưa có $BIN_SRC, build bằng go..."
		(cd "$AGENT_DIR" && CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w" -o "$BIN_SRC" ./cmd/scs-agent)
	else
		die "Không có binary $BIN_SRC" "Build trên máy khác: (cd agent && CGO_ENABLED=0 GOOS=linux go build -o bin/scs-agent ./cmd/scs-agent) rồi chép sang, hoặc dùng --binary"
	fi
fi
if ver="$("$BIN_SRC" version 2>/dev/null)"; then
	ok "Binary: $BIN_SRC (phiên bản $ver)"
else
	die "$BIN_SRC không chạy được trên máy này" "Build đúng kiến trúc: GOARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
fi

# Hostname theo quy ước <mã>-TN<số>[-PRI|-SEC]
host="$(hostname)"
if "$BIN_SRC" fingerprint -config /dev/null 2>/dev/null | grep -q "SAI QUY ƯỚC"; then
	warn "Hostname \"$host\" sai quy ước (đúng: 12345-TN1-PRI, 12345-TN2-SEC, 12345-TN3...). Vẫn cài được, admin sẽ thấy cảnh báo; nên sửa: hostnamectl set-hostname <tên>"
else
	ok "Hostname $host đúng quy ước"
fi

# Đồng hồ (gói tin lệch quá 5 phút bị server từ chối; agent tự bù nhưng nên bật NTP)
if [[ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null)" == yes ]]; then
	ok "Đồng hồ đã đồng bộ NTP"
else
	warn "Đồng hồ chưa đồng bộ NTP; bật: timedatectl set-ntp true"
fi

# Kết nối tới server (kiểm luôn chứng chỉ server bằng đúng CA này)
if command -v curl >/dev/null; then
	if out="$(curl -sS --max-time 10 --cacert "$CA_FILE" "$SERVER/healthz" 2>&1)" && [[ "$out" == *'"status":"ok"'* ]]; then
		ok "Kết nối được server $SERVER (chứng chỉ server khớp CA)"
	else
		err "Không kết nối được $SERVER/healthz: ${out:0:200}"
		echo "      Kiểm tra: IP/cổng, chuyển tiếp cổng trên router phía server, firewall; chứng chỉ server phải ghi đúng IP này."
	fi
else
	warn "Không có curl, bỏ qua kiểm tra kết nối"
fi

if [[ $MODE == check ]]; then
	step "Kết quả kiểm tra"
	echo "  Lỗi: $ERRORS, cảnh báo: $WARNINGS. Chưa cài gì (chế độ --check)."
	[[ $ERRORS -eq 0 ]] || exit 1
	exit 0
fi
if [[ $ERRORS -gt 0 && $YES -eq 0 ]]; then
	read -r -p "  Có $ERRORS lỗi ở trên. Vẫn cài (agent sẽ tự thử lại khi server kết nối được)? [y/N] " a
	[[ "$a" =~ ^[yY]$ ]] || exit 1
fi

# ---------------------------------------------------------------- 2. cài đặt
step "2. Cài đặt"
if ! id "$SVC_USER" >/dev/null 2>&1; then
	useradd --system --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
	ok "Đã tạo user hệ thống $SVC_USER"
fi
for g in adm systemd-journal; do
	getent group "$g" >/dev/null && usermod -aG "$g" "$SVC_USER"
done

install -d -o root -g root -m 755 "$OPT_DIR"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 700 "$ETC_DIR" "$DATA_DIR"
install -o root -g root -m 755 "$BIN_SRC" "$OPT_DIR/scs-agent.new"
mv -f "$OPT_DIR/scs-agent.new" "$OPT_DIR/scs-agent"
ln -sf "$OPT_DIR/scs-agent" "$LINK"
ok "Binary: $OPT_DIR/scs-agent (lệnh scs-agent dùng được ở mọi nơi)"

if [[ "$(readlink -f "$CA_FILE")" != "$ETC_DIR/server-ca.pem" ]]; then
	install -o root -g "$SVC_USER" -m 644 "$CA_FILE" "$ETC_DIR/server-ca.pem"
fi
ok "CA của server: $ETC_DIR/server-ca.pem"

[[ -f "$ETC_DIR/agent.env" ]] && cp -p "$ETC_DIR/agent.env" "$ETC_DIR/agent.env.bak"
if [[ -z "$WATCH" && -r "$ETC_DIR/agent.env.bak" ]]; then
	WATCH="$(sed -n 's/^SCS_WATCH_SERVICES=//p' "$ETC_DIR/agent.env.bak" | tail -n1)"
fi
umask 027
cat >"$ETC_DIR/agent.env" <<EOF
# Cấu hình SCS-Agent (tạo bởi scripts/install.sh; sửa xong: systemctl restart scs-agent)
SCS_SERVER=$SERVER
SCS_SERVER_CA=$ETC_DIR/server-ca.pem
SCS_KEY_DIR=$ETC_DIR
SCS_STATE_DIR=$DATA_DIR
SCS_WATCH_SERVICES=$WATCH
SCS_LOG_LEVEL=info
EOF
chown root:"$SVC_USER" "$ETC_DIR/agent.env"
chmod 640 "$ETC_DIR/agent.env"
ok "Cấu hình: $ETC_DIR/agent.env"

install -o root -g root -m 644 "$UNIT_SRC" "$UNIT_DST"
systemctl daemon-reload
systemctl enable scs-agent >/dev/null
systemctl restart scs-agent
ok "Đã cài dịch vụ scs-agent, tự chạy khi khởi động máy"

# ---------------------------------------------------------------- 3. kết quả
step "3. Kết quả"
for _ in $(seq 1 15); do
	[[ -f "$ETC_DIR/key.pem" ]] && break
	sleep 1
done
if ! systemctl is-active --quiet scs-agent; then
	journalctl -u scs-agent -n 20 --no-pager >&2 || true
	die "scs-agent không chạy được" "Xem log phía trên; sửa $ETC_DIR/agent.env rồi: systemctl restart scs-agent"
fi
ok "scs-agent đang chạy"
keyid="$("$OPT_DIR/scs-agent" fingerprint -config "$ETC_DIR/agent.env" 2>/dev/null | sed -n 's/^Mã khóa máy *: *\([0-9a-f]\{16\}\).*/\1/p')"
state="$(sed -n 's/.*"status": *"\([a-z]*\)".*/\1/p' "$DATA_DIR/state.json" 2>/dev/null || true)"
cat <<EOF

  Hostname       : $host
  Mã khóa máy    : ${keyid:-chưa có (agent đang khởi động, xem: scs-agent fingerprint)}
  Trạng thái     : ${state:-đang gửi đăng ký}

  Việc tiếp theo: admin vào web quản trị, trang "Chờ duyệt", đối chiếu MÃ KHÓA ở trên rồi bấm Duyệt.
  Agent tự hỏi lại mỗi khoảng 5 phút và nhận chứng chỉ sau khi được duyệt.

  Xem log      : journalctl -u scs-agent -f
  Trạng thái   : scs-agent status
  Gỡ cài       : sudo bash $0 --uninstall
EOF
