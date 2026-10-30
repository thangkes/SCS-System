#!/usr/bin/env bash
# menu.sh - menu cài đặt và vận hành SCS-Server. Chọn số, script hỏi những gì cần.
#
#   bash server/menu.sh
#
# Chạy bằng user thường hoặc root: thư mục code thuộc user nào thì script chạy bằng user đó;
# bước nào cần quyền quản trị script tự gọi sudo.
# Các script bên dưới (scripts/setup.sh, install.sh, update.sh) vẫn dùng trực tiếp bằng cờ lệnh được
# khi cần tự động hóa; xem --help của từng script.

# Chạy bằng sh (dash) thì tự chuyển sang bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Euo pipefail # không -e: một bước lỗi thì quay về menu, không thoát

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS="$SERVER_DIR/scripts"
ENV_FILE=/etc/scs-server/server.env

if [[ -t 1 ]]; then
	C_B=$'\e[1m' C_DIM=$'\e[2m' C_GRN=$'\e[32m' C_YEL=$'\e[33m' C_RED=$'\e[31m' C_0=$'\e[0m'
else
	C_B='' C_DIM='' C_GRN='' C_YEL='' C_RED='' C_0=''
fi

# Chạy bằng root: repo thuộc user khác thì chuyển sang user đó (git và build chạy bằng chủ repo,
# bước cần quyền vẫn tự gọi sudo); repo thuộc root thì chạy luôn bằng root.
if [[ $EUID -eq 0 ]]; then
	owner="$(stat -c %U "$SERVER_DIR/..")"
	if [[ "$owner" != root ]]; then
		echo "Đang chạy bằng root, chuyển sang user sở hữu thư mục code: $owner"
		exec sudo -u "$owner" -H bash "${BASH_SOURCE[0]}" "$@"
	fi
fi
[[ -t 0 ]] || { echo "menu.sh cần chạy trong terminal. Tự động hóa: dùng trực tiếp scripts/*.sh với cờ lệnh (--help)."; exit 1; }

# ask CÂU_HỎI [MẶC_ĐỊNH]: hỏi, Enter để lấy mặc định; kết quả ở REPLY.
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

# env_get KEY: đọc giá trị trong server.env (file quyền 600 của user scs nên cần sudo).
env_get() {
	sudo -n true 2>/dev/null || return 0
	sudo grep -E "^$1=" "$ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2- | sed -E 's/[[:space:]]+#.*$//; s/^"//; s/"$//'
}

lan_ip() { ip route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}'; }

# lan_cidr: dải mạng của card mạng chính, ví dụ 192.168.1.0/24.
lan_cidr() {
	local dev
	dev="$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "dev") { print $(i + 1); exit }}')"
	[[ -n "$dev" ]] || return 0
	ip -o -4 route show dev "$dev" scope link 2>/dev/null | awk '$1 ~ /\// { print $1; exit }'
}

valid_ip() { [[ "$1" =~ ^[0-9]+([.][0-9]+){3}$ || "$1" =~ ^[0-9a-fA-F]*:[0-9a-fA-F:]*$ ]]; }
valid_cidr() { [[ "$1" =~ ^[0-9]+([.][0-9]+){3}(/[0-9]{1,2})?$ ]]; }

status_line() {
	local svc ver
	if systemctl is-active --quiet scs-server 2>/dev/null; then
		svc="${C_GRN}đang chạy${C_0}"
	elif systemctl list-unit-files scs-server.service >/dev/null 2>&1 && [[ -f /etc/systemd/system/scs-server.service ]]; then
		svc="${C_RED}đã cài nhưng không chạy${C_0}"
	else
		svc="${C_YEL}chưa cài${C_0}"
	fi
	ver="$(/opt/scs-server/scs-server version 2>/dev/null || echo "—")"
	printf '  Dịch vụ: %s   Phiên bản: %s   Máy này: %s\n' "$svc" "$ver" "$(lan_ip || true)"
}

# ---------------------------------------------------------------- các bước
do_check() { bash "$SCRIPTS/setup.sh" --check; }

do_setup() { bash "$SCRIPTS/setup.sh" --start --test; }

do_install() {
	local args=() wan lan vpn allow
	echo "  Thông tin để tạo chứng chỉ server (bỏ trống phần không dùng)."
	echo "  ${C_DIM}Đã có CA từ lần cài trước thì các IP chỉ dùng khi cấp lại chứng chỉ (mục 7).${C_0}"
	ask "IP WAN của server (agent ở điểm bán gọi tới; xem bằng: curl -s ifconfig.me)" ""
	wan="$REPLY"
	if [[ -n "$wan" ]] && ! valid_ip "$wan"; then echo "  ${C_RED}\"$wan\" không phải IP${C_0}"; return 1; fi
	ask "IP LAN của server (máy trong mạng nội bộ vào web)" "$(lan_ip || true)"
	lan="$REPLY"
	if [[ -n "$lan" ]] && ! valid_ip "$lan"; then echo "  ${C_RED}\"$lan\" không phải IP${C_0}"; return 1; fi
	ask "IP VPN của server (nếu admin vào qua VPN)" ""
	vpn="$REPLY"
	if [[ -n "$vpn" ]] && ! valid_ip "$vpn"; then echo "  ${C_RED}\"$vpn\" không phải IP${C_0}"; return 1; fi
	ask "Dải mạng được vào web quản trị 7222 (Enter = dải LAN; '-' = chỉ máy server)" "$(lan_cidr || true)"
	allow="$REPLY"
	if [[ -n "$allow" && "$allow" != "-" ]] && ! valid_cidr "$allow"; then echo "  ${C_RED}\"$allow\" không phải dải CIDR${C_0}"; return 1; fi

	[[ -n "$wan" ]] && args+=(--wan-ip "$wan")
	[[ -n "$lan" ]] && args+=(--lan-ip "$lan")
	[[ -n "$vpn" ]] && args+=(--vpn-ip "$vpn")
	[[ -n "$allow" && "$allow" != "-" ]] && args+=(--admin-allow "$allow")
	echo
	echo "  Sẽ chạy: sudo bash scripts/install.sh ${args[*]}"
	confirm "Tiếp tục?" || return 0
	sudo bash "$SCRIPTS/install.sh" "${args[@]}"
}

do_update() { bash "$SCRIPTS/update.sh"; }

do_rollback() { bash "$SCRIPTS/update.sh" --rollback; }

do_allow() {
	local cur
	cur="$(env_get SCS_ADMIN_ALLOW)"
	[[ -n "$cur" ]] && echo "  Đang cho phép: $cur"
	ask "Dải mạng thêm vào (ví dụ 192.168.1.0/24, 10.8.0.0/24)" "$(lan_cidr || true)"
	valid_cidr "$REPLY" || { echo "  ${C_RED}\"$REPLY\" không phải dải CIDR${C_0}"; return 1; }
	sudo bash "$SCRIPTS/install.sh" --admin-allow "$REPLY"
}

do_reissue() {
	local args=() ips
	echo "  Liệt kê ĐỦ mọi IP cần có trong chứng chỉ (WAN, LAN, VPN), cách nhau bằng dấu cách."
	echo "  ${C_DIM}CA giữ nguyên nên agent không bị ảnh hưởng; trình duyệt sẽ thấy chứng chỉ mới.${C_0}"
	ask "Các IP" "$(lan_ip || true)"
	ips="$REPLY"
	for ip in $ips; do
		valid_ip "$ip" || { echo "  ${C_RED}\"$ip\" không phải IP${C_0}"; return 1; }
		args+=(--wan-ip "$ip")
	done
	[[ ${#args[@]} -gt 0 ]] || { echo "  Chưa nhập IP nào."; return 1; }
	confirm "Cấp lại chứng chỉ server cho: $ips ?" || return 0
	sudo bash "$SCRIPTS/install.sh" --reissue-cert "${args[@]}"
}

do_code() {
	if command -v scs-login-code >/dev/null; then
		scs-login-code
	else
		echo "  Chưa cài dịch vụ (chọn mục 3 trước)."
	fi
}

do_status() {
	systemctl status scs-server --no-pager -n 0 2>/dev/null | head -n 5
	echo
	sudo journalctl -u scs-server -n 30 --no-pager -o cat 2>/dev/null | cut -c1-220
}

do_uninstall() {
	confirm "Gỡ dịch vụ scs-server? (giữ cấu hình, CA, dữ liệu; cài lại bằng mục 3)" || return 0
	sudo bash "$SCRIPTS/install.sh" --uninstall
}

# ---------------------------------------------------------------- menu
while true; do
	clear 2>/dev/null || true
	printf '%s SCS-Server — menu cài đặt và vận hành%s\n\n' "$C_B" "$C_0"
	status_line
	cat <<EOF

  ${C_B}Cài đặt${C_0}
   1) Kiểm tra điều kiện máy (không cài gì)
   2) Chuẩn bị máy: Docker, Go, PostgreSQL + NATS, chạy test
   3) Cài / cài lại dịch vụ SCS-Server (tạo CA + chứng chỉ, mở web cho LAN)

  ${C_B}Cập nhật${C_0}
   4) Cập nhật lên bản mới (test, sao lưu DB nếu cần, tự quay về khi lỗi)
   5) Quay về bản trước

  ${C_B}Mạng và chứng chỉ${C_0}
   6) Cho thêm dải mạng vào web quản trị (7222)
   7) Cấp lại chứng chỉ server (đổi / thêm IP)

  ${C_B}Vận hành${C_0}
   8) Lấy mã đăng nhập web
   9) Trạng thái và log gần nhất
  10) Gỡ dịch vụ (giữ dữ liệu)

   0) Thoát
EOF
	echo
	read -r -p "  Chọn: " choice
	echo
	case "$choice" in
		1) do_check ;;
		2) do_setup ;;
		3) do_install ;;
		4) do_update ;;
		5) do_rollback ;;
		6) do_allow ;;
		7) do_reissue ;;
		8) do_code ;;
		9) do_status ;;
		10) do_uninstall ;;
		0 | q | Q | "") exit 0 ;;
		*) echo "  Không có mục \"$choice\"." ;;
	esac
	pause
done
