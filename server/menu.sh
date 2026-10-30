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

valid_dns() { [[ "$1" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ && "$1" == *.* ]]; }

# cert_names: các IP / tên miền đang có trong chứng chỉ server (trừ 127.0.0.1), cách nhau dấu cách.
cert_names() {
	local pem
	pem="$(env_get SCS_TLS_CERT)"
	[[ -n "$pem" ]] || return 0
	sudo openssl x509 -in "$pem" -noout -ext subjectAltName 2>/dev/null |
		grep -oE '(IP Address|DNS):[^, ]+' | sed -E 's/^(IP Address|DNS)://' | grep -vxE '127\.0\.0\.1|::1' | tr '\n' ' ' | sed 's/ $//'
}

# ask_names [LAN] [WAN...]: hỏi địa chỉ ghi vào chứng chỉ server. Chỉ IP LAN là bắt buộc;
# IP WAN / tên miền DDNS (agent từ Internet qua chuyển tiếp cổng) và VPN thêm sau bằng mục 7 được.
# Kết quả: mảng NAME_ARGS (--lan-ip / --wan-ip / --dns ...).
ask_names() {
	local lan ext x
	NAME_ARGS=()
	ask "IP LAN của server (nên đặt IP tĩnh hoặc giữ IP trên router)" "${1:-$(lan_ip || true)}"
	lan="$REPLY"
	valid_ip "$lan" || { echo "  ${C_RED}\"$lan\" không phải IP${C_0}"; return 1; }
	NAME_ARGS+=(--lan-ip "$lan")
	echo "  ${C_DIM}Agent gọi bằng địa chỉ nào cũng được (chỉ kiểm chứng chỉ do CA của server cấp). Các địa chỉ dưới${C_0}"
	echo "  ${C_DIM}được gửi cho agent làm dự phòng, và ghi vào chứng chỉ để trình duyệt không báo sai tên.${C_0}"
	ask "IP WAN, tên miền DDNS, IP VPN (tùy chọn, cách nhau dấu cách; '-' = không có)" "${2:-}"
	ext="$REPLY"
	[[ "$ext" == "-" ]] && ext=""
	for x in $ext; do
		if valid_ip "$x"; then
			[[ "$x" == "$lan" ]] || NAME_ARGS+=(--wan-ip "$x")
		elif valid_dns "$x"; then
			NAME_ARGS+=(--dns "$x")
		else
			echo "  ${C_RED}\"$x\" không phải IP hay tên miền${C_0}"
			return 1
		fi
	done
}

# ask_allow: ai được vào web quản trị 7222. Kết quả: mảng ALLOW_ARGS (--admin-allow ...).
ask_allow() {
	local x
	ALLOW_ARGS=()
	echo "  Ai được vào web quản trị (cổng 7222)? Vẫn phải đăng nhập bằng mã 6 số."
	echo "   1) Toàn mạng nội bộ: mọi dải IP riêng 10.x, 172.16-31.x, 192.168.x (công ty nhiều lớp mạng / VLAN)"
	echo "   2) Chỉ dải LAN của máy server ($(lan_cidr || echo '?'))"
	echo "   3) Tự nhập các dải (ví dụ 10.10.0.0/16 10.20.5.0/24)"
	echo "   4) Chỉ chính máy server"
	ask "Chọn" "1"
	case "$REPLY" in
		1) ALLOW_ARGS=(--admin-allow private) ;;
		2) x="$(lan_cidr || true)"
			[[ -n "$x" ]] || { echo "  ${C_RED}Không dò được dải LAN${C_0}"; return 1; }
			ALLOW_ARGS=(--admin-allow "$x") ;;
		3) ask "Các dải, cách nhau dấu cách" ""
			for x in $REPLY; do
				valid_cidr "$x" || { echo "  ${C_RED}\"$x\" không phải dải CIDR${C_0}"; return 1; }
				[[ "$x" == 0.0.0.0/0 ]] && { echo "  ${C_RED}Không mở web quản trị cho cả Internet${C_0}"; return 1; }
				ALLOW_ARGS+=(--admin-allow "$x")
			done
			[[ ${#ALLOW_ARGS[@]} -gt 0 ]] || { echo "  Chưa nhập dải nào."; return 1; }
			;;
		4) ;;
		*) echo "  Không có lựa chọn \"$REPLY\"."; return 1 ;;
	esac
}

# agent_servers_arg: từ NAME_ARGS dựng danh sách địa chỉ gửi cho agent (--agent-servers).
# IP LAN dùng cổng agent của server; IP WAN / tên miền dùng cổng chuyển tiếp trên router (hỏi).
# Kết quả: mảng SERVERS_ARGS.
agent_servers_arg() {
	local port fwd list="" i=0 kind val
	port="$(env_get SCS_INGEST_LISTEN)"
	port="${port##*:}"
	port="${port:-6222}"
	SERVERS_ARGS=()
	while [[ $i -lt ${#NAME_ARGS[@]} ]]; do
		kind="${NAME_ARGS[$i]}" val="${NAME_ARGS[$((i + 1))]}"
		i=$((i + 2))
		if [[ "$kind" == --lan-ip ]]; then
			list="${list:+$list,}https://$val:$port"
		else
			if [[ -z "${fwd:-}" ]]; then
				ask "Cổng trên router chuyển tiếp về $port (agent từ Internet gọi vào cổng này)" "$port"
				fwd="$REPLY"
				[[ "$fwd" =~ ^[0-9]{1,5}$ ]] || { echo "  ${C_RED}\"$fwd\" không phải số cổng${C_0}"; return 1; }
			fi
			[[ "$val" == *:* ]] && val="[$val]"
			list="${list:+$list,}https://$val:$fwd"
		fi
	done
	SERVERS_ARGS=(--agent-servers "$list")
	echo "  ${C_DIM}Agent sẽ nhận danh sách địa chỉ: $list (tự chuyển khi một địa chỉ mất kết nối)${C_0}"
}

do_install() {
	local args=()
	echo "  Địa chỉ ghi vào chứng chỉ server."
	echo "  ${C_DIM}Đã có CA từ lần cài trước thì địa chỉ chỉ dùng khi cấp lại chứng chỉ (mục 7).${C_0}"
	ask_names || return 1
	args+=("${NAME_ARGS[@]}")
	agent_servers_arg || return 1
	args+=("${SERVERS_ARGS[@]}")
	ask_allow || return 1
	args+=("${ALLOW_ARGS[@]}")
	echo
	echo "  Sẽ chạy: sudo bash scripts/install.sh ${args[*]}"
	confirm "Tiếp tục?" || return 0
	sudo bash "$SCRIPTS/install.sh" "${args[@]}"
}

# do_update: cập nhật xong mà menu.sh đã đổi thì nạp lại menu (bản đang chạy vẫn là code cũ).
do_update() {
	local before
	before="$(sha256sum "${BASH_SOURCE[0]}" | cut -d' ' -f1)"
	bash "$SCRIPTS/update.sh" || return 1
	if [[ "$(sha256sum "${BASH_SOURCE[0]}" | cut -d' ' -f1)" != "$before" ]]; then
		echo
		echo "  ${C_GRN}Menu có bản mới, nạp lại...${C_0}"
		pause
		exec bash "${BASH_SOURCE[0]}"
	fi
}

do_rollback() { bash "$SCRIPTS/update.sh" --rollback; }

do_allow() {
	local cur
	cur="$(env_get SCS_ADMIN_ALLOW)"
	[[ -n "$cur" ]] && echo "  Đang cho phép: $cur"
	ask_allow || return 1
	[[ ${#ALLOW_ARGS[@]} -gt 0 ]] || { echo "  Không thêm dải nào."; return 0; }
	sudo bash "$SCRIPTS/install.sh" "${ALLOW_ARGS[@]}"
}

do_reissue() {
	local cur x lan="" ext=""
	cur="$(cert_names)"
	echo "  Đang có trong chứng chỉ: ${cur:-(chưa đọc được)}"
	echo "  ${C_DIM}Dùng khi đổi IP LAN hoặc thêm IP WAN / tên miền: agent đã duyệt nhận danh sách địa chỉ mới${C_0}"
	echo "  ${C_DIM}trong vòng 5 phút. CA giữ nguyên nên agent đã cài vẫn kết nối bình thường.${C_0}"
	# Tách địa chỉ đang có: IP LAN (dải riêng) làm mặc định câu đầu, còn lại làm mặc định câu sau.
	for x in $cur; do
		if [[ -z "$lan" && "$x" =~ ^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.) ]]; then lan="$x"; else ext="${ext:+$ext }$x"; fi
	done
	ask_names "$lan" "$ext" || return 1
	agent_servers_arg || return 1
	confirm "Cấp lại chứng chỉ server với: ${NAME_ARGS[*]} ?" || return 0
	sudo bash "$SCRIPTS/install.sh" --reissue-cert "${NAME_ARGS[@]}" "${SERVERS_ARGS[@]}"
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

do_package() {
	local last="$SERVER_DIR/data/dist/last-server.txt" def="" watch arch
	[[ -r "$last" ]] && def="$(head -n1 "$last")"
	if [[ -z "$def" ]]; then
		local port
		port="$(env_get SCS_INGEST_LISTEN)"
		port="${port##*:}"
		def="https://$(lan_ip || echo 127.0.0.1):${port:-6222}"
	fi
	echo "  Gói gồm binary agent, script cài, CA của server và địa chỉ server đặt sẵn: máy tại điểm bán"
	echo "  chỉ cần giải nén rồi chạy cài, không phải nhập gì."
	echo "  ${C_DIM}Trong mạng nội bộ: https://<IP LAN>:6222. Từ Internet: https://<IP WAN hoặc tên miền>:<cổng chuyển tiếp về 6222>.${C_0}"
	ask "Địa chỉ agent gọi tới" "$def"
	[[ "$REPLY" =~ ^https://[^/:@[:space:]]+:[0-9]{1,5}/?$ ]] ||
		{ echo "  ${C_RED}Phải có dạng https://<IP>:<cổng>, ví dụ https://192.168.1.10:6222${C_0}"; return 1; }
	local server="$REPLY"
	ask "Dịch vụ mặc định cần báo trạng thái, cách nhau dấu phẩy (bỏ trống nếu không có)" ""
	watch="$REPLY"
	ask "Kiến trúc máy tại điểm bán (amd64 / arm64)" "amd64"
	arch="$REPLY"
	if bash "$SCRIPTS/package-agent.sh" --server "$server" --watch "$watch" --arch "$arch"; then
		mkdir -p "$(dirname "$last")"
		printf '%s\n' "$server" >"$last"
	fi
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
   6) Cho thêm dải mạng vào web quản trị (7222), kể cả toàn mạng nội bộ
   7) Đổi / thêm địa chỉ server (IP LAN, IP WAN, tên miền): cấp lại chứng chỉ, báo cho agent

  ${C_B}Vận hành${C_0}
   8) Lấy mã đăng nhập web
   9) Trạng thái và log gần nhất
  10) Gỡ dịch vụ (giữ dữ liệu)

  ${C_B}Agent${C_0}
  11) Đóng gói agent để cài cho máy tại điểm bán (một file .tar.gz)

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
		11) do_package ;;
		0 | q | Q | "") exit 0 ;;
		*) echo "  Không có mục \"$choice\"." ;;
	esac
	pause
done
