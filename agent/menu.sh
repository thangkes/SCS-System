#!/usr/bin/env bash
# menu.sh - menu cài đặt và vận hành SCS-Agent trên máy tại điểm bán. Chọn số, script hỏi những gì cần.
#
#   sudo bash agent/menu.sh
#
# scripts/install.sh bên dưới vẫn dùng trực tiếp bằng cờ lệnh được khi cần cài hàng loạt (xem --help).

# Chạy bằng sh (dash) thì tự chuyển sang bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Euo pipefail # không -e: một bước lỗi thì quay về menu, không thoát

AGENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL="$AGENT_DIR/scripts/install.sh"
CONF=/etc/scs-agent/agent.env

if [[ -t 1 ]]; then
	C_B=$'\e[1m' C_GRN=$'\e[32m' C_YEL=$'\e[33m' C_RED=$'\e[31m' C_0=$'\e[0m'
else
	C_B='' C_GRN='' C_YEL='' C_RED='' C_0=''
fi

[[ $EUID -eq 0 ]] || { echo "Cần quyền root: sudo bash agent/menu.sh"; exit 1; }
[[ -t 0 ]] || { echo "menu.sh cần chạy trong terminal. Cài hàng loạt: dùng scripts/install.sh với cờ lệnh (--help)."; exit 1; }

ask() {
	local a
	if [[ -n "${2:-}" ]]; then
		read -r -p "  $1 [$2]: " a
		REPLY="${a:-$2}"
	else
		read -r -p "  $1: " a
		REPLY="$a"
	fi
}

confirm() {
	local a
	read -r -p "  $1 [y/N] " a
	[[ "$a" =~ ^[yY]$ ]]
}

pause() {
	echo
	read -r -p "  Nhấn Enter để quay lại menu..." _
}

conf_get() { [[ -r "$CONF" ]] && sed -n "s/^$1=//p" "$CONF" | tail -n1; }

# default_ca: CA đang cài, hoặc file agent-ca.pem / ca.pem nằm cạnh thư mục agent.
default_ca() {
	local f
	for f in /etc/scs-agent/server-ca.pem "$AGENT_DIR/../agent-ca.pem" "$AGENT_DIR/agent-ca.pem" "$PWD/agent-ca.pem" "$PWD/ca.pem"; do
		[[ -r "$f" ]] && { readlink -f "$f"; return; }
	done
}

status_line() {
	local svc state
	if systemctl is-active --quiet scs-agent 2>/dev/null; then
		svc="${C_GRN}đang chạy${C_0}"
	elif [[ -f /etc/systemd/system/scs-agent.service ]]; then
		svc="${C_RED}đã cài nhưng không chạy${C_0}"
	else
		svc="${C_YEL}chưa cài${C_0}"
	fi
	state="$(sed -n 's/.*"status": *"\([a-z]*\)".*/\1/p' /var/lib/scs-agent/state.json 2>/dev/null || true)"
	case "$state" in
		new | "") state="chưa đăng ký" ;;
		pending) state="${C_YEL}chờ admin duyệt${C_0}" ;;
		approved) state="${C_GRN}đã duyệt${C_0}" ;;
		rejected) state="${C_RED}bị từ chối / thu hồi${C_0}" ;;
	esac
	printf '  Dịch vụ: %s   Đăng ký: %s   Hostname: %s\n' "$svc" "$state" "$(hostname)"
	printf '  Server: %s\n' "$(conf_get SCS_SERVER || true)"
}

# ask_target: hỏi địa chỉ server và CA (mặc định lấy từ cấu hình đang có); kết quả ở SERVER, CA.
ask_target() {
	ask "Địa chỉ server (https://<IP WAN>:<cổng>)" "$(conf_get SCS_SERVER || true)"
	SERVER="$REPLY"
	[[ "$SERVER" =~ ^https://[^/:@[:space:]]+:[0-9]{1,5}/?$ ]] ||
		{ echo "  ${C_RED}Phải có dạng https://<IP>:<cổng>, ví dụ https://203.0.113.10:16222${C_0}"; return 1; }
	ask "File CA của server (agent-ca.pem lấy từ máy server)" "$(default_ca || true)"
	CA="$REPLY"
	[[ -r "$CA" ]] || { echo "  ${C_RED}Không đọc được \"$CA\"${C_0}"; return 1; }
}

# ---------------------------------------------------------------- các bước
do_check() {
	ask_target || return 1
	bash "$INSTALL" --check --server "$SERVER" --ca "$CA"
}

do_install() {
	local watch
	ask_target || return 1
	ask "Dịch vụ cần báo trạng thái, cách nhau dấu phẩy (bỏ trống nếu không có)" "$(conf_get SCS_WATCH_SERVICES || true)"
	watch="$REPLY"
	local args=(--server "$SERVER" --ca "$CA")
	[[ -n "$watch" ]] && args+=(--watch "$watch")
	echo
	echo "  Sẽ chạy: bash scripts/install.sh ${args[*]}"
	confirm "Tiếp tục?" || return 0
	bash "$INSTALL" "${args[@]}"
}

do_status() {
	if command -v scs-agent >/dev/null; then scs-agent status; else echo "  Chưa cài agent (chọn mục 2)."; fi
}

do_fingerprint() {
	if command -v scs-agent >/dev/null; then
		scs-agent fingerprint
	elif [[ -x "$AGENT_DIR/bin/scs-agent" ]]; then
		"$AGENT_DIR/bin/scs-agent" fingerprint -config /dev/null
	else
		echo "  Không có binary scs-agent."
	fi
}

do_log() { journalctl -u scs-agent -n 40 --no-pager -o cat 2>/dev/null | cut -c1-220; }

do_restart() {
	systemctl restart scs-agent && echo "  Đã khởi động lại scs-agent." || echo "  ${C_RED}Không khởi động lại được (đã cài chưa?)${C_0}"
}

do_uninstall() {
	confirm "Gỡ dịch vụ scs-agent? (GIỮ khóa máy: cài lại không phải duyệt lại)" || return 0
	bash "$INSTALL" --uninstall
}

do_purge() {
	echo "  ${C_YEL}Xóa cả khóa máy và chứng chỉ: máy phải đăng ký và được admin duyệt lại.${C_0}"
	confirm "Gỡ hết?" || return 0
	bash "$INSTALL" --uninstall --purge --yes
}

# ---------------------------------------------------------------- menu
while true; do
	clear 2>/dev/null || true
	printf '%s SCS-Agent — menu cài đặt và vận hành%s\n\n' "$C_B" "$C_0"
	status_line
	cat <<EOF

  ${C_B}Cài đặt${C_0}
   1) Kiểm tra trước khi cài (hostname, đồng hồ, kết nối tới server)
   2) Cài / cài lại agent

  ${C_B}Theo dõi${C_0}
   3) Trạng thái đăng ký và thông tin máy sẽ gửi
   4) Hostname và mã khóa máy (đối chiếu khi admin duyệt)
   5) Log gần nhất
   6) Khởi động lại agent

  ${C_B}Gỡ cài${C_0}
   7) Gỡ dịch vụ (giữ khóa máy)
   8) Gỡ hết (máy phải duyệt lại)

   0) Thoát
EOF
	echo
	read -r -p "  Chọn: " choice
	echo
	case "$choice" in
		1) do_check ;;
		2) do_install ;;
		3) do_status ;;
		4) do_fingerprint ;;
		5) do_log ;;
		6) do_restart ;;
		7) do_uninstall ;;
		8) do_purge ;;
		0 | q | Q | "") exit 0 ;;
		*) echo "  Không có mục \"$choice\"." ;;
	esac
	pause
done
