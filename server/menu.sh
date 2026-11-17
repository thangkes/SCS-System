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
# Phiên bản code của chính menu đang chạy, để biết menu đã nạp bản mới chưa sau khi cập nhật.
MENU_VER="$(git -C "$SERVER_DIR" describe --tags --always --dirty 2>/dev/null || echo '?')"

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
	rules_status_line
}

# rules_status_line: báo các bộ rule đang chờ ký (không hỏi mật khẩu sudo; không đọc được thì im lặng).
rules_status_line() {
	[[ -x /opt/scs-server/scs-server ]] || return 0
	local list n names
	list="$(cd / && timeout 10 sudo -n -u scs /opt/scs-server/scs-server rules pending -env-file "$ENV_FILE" 2>/dev/null || true)"
	[[ -n "$list" ]] || return 0
	n="$(grep -c . <<<"$list")"
	names="$(cut -f1 <<<"$list" | head -n 3 | paste -sd ',' - | sed 's/,/, /g')"
	((n > 3)) && names+=", …"
	printf '  %sRule: %s bộ chờ duyệt và ký%s: %s (mục 16, hoặc web trang "Rule quét log")\n' "$C_YEL" "$n" "$C_0" "$names"
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

# ask_names [LAN] [THÊM] [HỎI_THÊM]: địa chỉ ghi vào chứng chỉ server và gửi cho agent làm dự phòng.
# Chỉ IP LAN là bắt buộc. HỎI_THÊM=1 thì hỏi thêm IP WAN / tên miền / IP VPN (mặc định THÊM);
# không thì giữ nguyên THÊM, không hỏi. Kết quả: mảng NAME_ARGS (--lan-ip / --wan-ip / --dns ...).
ask_names() {
	local lan ext="${2:-}" x
	NAME_ARGS=()
	ask "IP LAN của server (nên đặt IP tĩnh hoặc giữ IP trên router)" "${1:-$(lan_ip || true)}"
	lan="$REPLY"
	valid_ip "$lan" || { echo "  ${C_RED}\"$lan\" không phải IP${C_0}"; return 1; }
	NAME_ARGS+=(--lan-ip "$lan")
	if [[ "${3:-0}" == 1 ]]; then
		ask "IP WAN, tên miền DDNS, IP VPN (cách nhau dấu cách; '-' = không có)" "$ext"
		ext="$REPLY"
	fi
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
	echo "   5) Cả Internet (mọi IP) — chỉ mã 6 số bảo vệ, người lạ có thể làm khóa đăng nhập"
	ask "Chọn" "1"
	case "$REPLY" in
		1) ALLOW_ARGS=(--admin-allow private) ;;
		2) x="$(lan_cidr || true)"
			[[ -n "$x" ]] || { echo "  ${C_RED}Không dò được dải LAN${C_0}"; return 1; }
			ALLOW_ARGS=(--admin-allow "$x") ;;
		3) ask "Các dải, cách nhau dấu cách" ""
			for x in $REPLY; do
				valid_cidr "$x" || { echo "  ${C_RED}\"$x\" không phải dải CIDR${C_0}"; return 1; }
				[[ "$x" == 0.0.0.0/0 || "$x" == ::/0 ]] && { echo "  Mở cho cả Internet: chọn mục 5."; return 1; }
				ALLOW_ARGS+=(--admin-allow "$x")
			done
			[[ ${#ALLOW_ARGS[@]} -gt 0 ]] || { echo "  Chưa nhập dải nào."; return 1; }
			;;
		4) ;;
		5) echo "  ${C_RED}Web quản trị sẽ mở cho mọi IP trên Internet.${C_0}"
			echo "  - Chỉ có mã 6 số bảo vệ; ai cũng gửi mã sai được để khóa đăng nhập"
			echo "    (mở khóa trên máy server: sudo scs-login-code -unlock)."
			echo "  - Chứng chỉ tự ký: trình duyệt luôn cảnh báo; chỉ bỏ qua khi chắc chắn đúng server."
			echo "  - Router phải chuyển tiếp cổng 7222 tới máy này thì ngoài Internet mới vào được."
			ask "Gõ INTERNET để xác nhận" ""
			[[ "$REPLY" == INTERNET ]] || { echo "  Chưa xác nhận, không đổi gì."; return 1; }
			ALLOW_ARGS=(--admin-allow internet) ;;
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
	echo "  ${C_DIM}Agent ở mọi lớp mạng gọi được server bằng IP này (không cần IP WAN). Cần mở cho agent ngoài${C_0}"
	echo "  ${C_DIM}Internet thì sau này dùng mục 7.${C_0}"
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
	local more=0
	if [[ -n "$ext" ]] || confirm "Thêm địa chỉ Internet (IP WAN / tên miền) cho agent ở ngoài mạng nội bộ?"; then more=1; fi
	ask_names "$lan" "$ext" "$more" || return 1
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
	local last="$SERVER_DIR/data/dist/last-server.txt" def="" arch
	[[ -r "$last" ]] && def="$(head -n1 "$last")"
	if [[ -z "$def" ]]; then
		local port
		port="$(env_get SCS_INGEST_LISTEN)"
		port="${port##*:}"
		def="https://$(lan_ip || echo 127.0.0.1):${port:-6222}"
	fi
	echo "  Gói là bản agent khung: binary, script cài, CA và khóa phát hành của server, địa chỉ server."
	echo "  Máy tại điểm bán chỉ cần giải nén rồi chạy cài. Duyệt xong, agent tự nhận cấu hình và bản mới"
	echo "  từ server (trang \"Agent\" trên web), không phải đóng gói hay cài lại."
	echo "  ${C_DIM}Trong mạng nội bộ: https://<IP LAN>:6222. Từ Internet: https://<IP WAN hoặc tên miền>:<cổng chuyển tiếp về 6222>.${C_0}"
	ask "Địa chỉ agent gọi tới" "$def"
	[[ "$REPLY" =~ ^https://[^/:@[:space:]]+:[0-9]{1,5}/?$ ]] ||
		{ echo "  ${C_RED}Phải có dạng https://<IP>:<cổng>, ví dụ https://192.168.1.10:6222${C_0}"; return 1; }
	local server="$REPLY"
	ask "Kiến trúc máy tại điểm bán (amd64 / arm64)" "amd64"
	arch="$REPLY"
	if bash "$SCRIPTS/package-agent.sh" --server "$server" --arch "$arch"; then
		mkdir -p "$(dirname "$last")"
		printf '%s\n' "$server" >"$last"
	fi
}

# ---------------------------------------------------------------- cụm nhiều server
SCS_BIN=/opt/scs-server/scs-server
SCS_HOME=/var/lib/scs-server

# cluster_dir: thư mục cụm (khóa node + nhật ký), luôn là đường dẫn tuyệt đối. Giá trị tương đối
# trong server.env tính từ thư mục làm việc của dịch vụ, không phải từ nơi đang đứng gõ lệnh.
cluster_dir() {
	local d
	d="$(env_get SCS_CLUSTER_DIR)"
	d="${d:-data/cluster}"
	[[ "$d" == /* ]] || d="$SCS_HOME/$d"
	printf '%s\n' "$d"
}

cluster_port() {
	local p
	p="$(env_get SCS_CLUSTER_LISTEN)"
	p="${p##*:}"
	printf '%s\n' "${p:-5222}"
}

# cluster_cmd LỆNH [THAM SỐ...]: chạy "scs-server cluster ..." bằng user của dịch vụ, đúng thư mục cụm.
cluster_cmd() {
	[[ -x "$SCS_BIN" ]] || { echo "  Chưa cài dịch vụ (chọn mục 3 trước)."; return 1; }
	# Chạy từ "/": scs-server tự nạp ./.env nếu có, mà .env của thư mục code (quyền 600) user scs không đọc được.
	local dir; dir="$(cluster_dir)"
	(cd / && sudo -u scs "$SCS_BIN" cluster "$@" -dir "$dir")
}

# cluster_state: none | joining | member
cluster_state() {
	local d
	d="$(cluster_dir)"
	if sudo test -s "$d/log.jsonl"; then
		echo member
	elif sudo test -f "$d/join.json"; then
		echo joining
	else
		echo none
	fi
}

valid_node_name() { [[ "$1" =~ ^[a-z0-9][a-z0-9._-]{0,31}$ ]]; }
valid_cluster_url() { [[ "$1" =~ ^https://[^/:@[:space:]]+:[0-9]{1,5}$ ]]; }

# default_node_name: hostname viết thường, bỏ ký tự không hợp lệ.
default_node_name() {
	hostname 2>/dev/null | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9._-]+/-/g; s/^[^a-z0-9]+//' | cut -c1-32
}

# ask_cluster_addrs: hỏi địa chỉ cổng cụm của server này. Kết quả: mảng ADDR_ARGS (-addr ... -addr ...).
ask_cluster_addrs() {
	local port a
	port="$(cluster_port)"
	ADDR_ARGS=()
	echo "  Địa chỉ để server khác gọi tới cổng cụm của server này (cổng $port)."
	echo "  ${C_DIM}Cùng mạng nội bộ: IP LAN. Khác địa điểm: thêm IP WAN / tên miền DDNS / IP VPN và cổng chuyển tiếp về $port.${C_0}"
	ask "Địa chỉ chính" "https://$(lan_ip || echo 127.0.0.1):$port"
	valid_cluster_url "$REPLY" || { echo "  ${C_RED}Phải có dạng https://<IP hoặc tên miền>:<cổng>${C_0}"; return 1; }
	ADDR_ARGS+=(-addr "$REPLY")
	ask "Địa chỉ thêm, cách nhau dấu cách ('-' = không có)" "-"
	[[ "$REPLY" == "-" ]] && return 0
	for a in $REPLY; do
		valid_cluster_url "$a" || { echo "  ${C_RED}\"$a\" phải có dạng https://<IP hoặc tên miền>:<cổng>${C_0}"; return 1; }
		ADDR_ARGS+=(-addr "$a")
	done
}

# cluster_after: khởi động lại dịch vụ để mở cổng cụm, mở ufw nếu đang bật.
cluster_after() {
	local port
	port="$(cluster_port)"
	if command -v ufw >/dev/null && sudo ufw status 2>/dev/null | grep -q "Status: active"; then
		sudo ufw allow "$port/tcp" comment "SCS cum server" >/dev/null && echo "  ${C_GRN}✔${C_0} ufw: đã mở cổng cụm $port"
	fi
	if sudo systemctl restart scs-server; then
		echo "  ${C_GRN}✔${C_0} Đã khởi động lại scs-server (mở cổng cụm $port)"
	else
		echo "  ${C_RED}Không khởi động lại được scs-server${C_0} (xem mục 9)"
	fi
}

# ---- mục 15: danh sách địa chỉ server gửi cho agent (thêm / gỡ khi đổi mạng, không khởi động lại)
servers_file() {
	local f
	f="$(env_get SCS_AGENT_SERVERS_FILE)"
	echo "${f:-/var/lib/scs-server/data/agent-servers.json}"
}

servers_cmd() {
	[[ -x "$SCS_BIN" ]] || { echo "  Chưa cài dịch vụ (chọn mục 3 trước)."; return 1; }
	# Chạy từ "/" (xem cluster_cmd).
	local f; f="$(servers_file)"
	(cd / && sudo -u scs "$SCS_BIN" servers "$@" -file "$f")
}

do_servers() {
	local c url note
	while true; do
		echo
		servers_cmd list || return 0
		echo
		echo "  ${C_DIM}Đổi địa điểm / đổi mạng: thêm địa chỉ mới TRƯỚC khi chuyển, chờ agent nhận (≤ 10 phút; web,${C_0}"
		echo "  ${C_DIM}trang \"Địa chỉ server\" cho biết bao nhiêu máy đã nhận), chuyển mạng, rồi mới gỡ địa chỉ cũ.${C_0}"
		echo "  ${C_DIM}Không phải cấp lại chứng chỉ, không phải khởi động lại server.${C_0}"
		echo
		echo "   a) Thêm địa chỉ    g) Gỡ địa chỉ    0) Quay lại"
		read -r -p "  Chọn: " c
		case "$c" in
			a | A)
				ask "Địa chỉ mới (https://<IP WAN hoặc tên miền>:<cổng chuyển tiếp về 6222>)" ""
				url="$REPLY"
				[[ -n "$url" ]] || continue
				[[ "$url" == https://* ]] || url="https://$url"
				ask "Ghi chú (ví dụ: Địa điểm mới - VNPT; Enter để bỏ qua)" "-"
				note="$REPLY"
				[[ "$note" == "-" ]] && note=""
				if confirm "Đặt lên đầu danh sách (agent thử địa chỉ này trước)?"; then
					servers_cmd add "$url" -note "$note" -first
				else
					servers_cmd add "$url" -note "$note"
				fi
				;;
			g | G)
				ask "Địa chỉ cần gỡ (gõ đúng như danh sách trên)" ""
				url="$REPLY"
				[[ -n "$url" ]] || continue
				echo "  ${C_DIM}Xem trên web (trang \"Địa chỉ server\") cột \"Máy đang dùng\" của địa chỉ này đã về 0 chưa.${C_0}"
				confirm "Gỡ $url khỏi danh sách gửi cho agent?" && servers_cmd remove "$url"
				;;
			0 | q | Q | "") return 0 ;;
			*) echo "  Không có mục \"$c\"." ;;
		esac
	done
}

do_cluster_status() {
	cluster_cmd status || return 0
	case "$(cluster_state)" in
		member) echo; echo "  ${C_DIM}Thêm / cô lập / gỡ server: web quản trị → trang \"Cụm server\" (phải làm trên từng server).${C_0}" ;;
		joining) echo; echo "  ${C_DIM}Admin duyệt trên web của mọi server trong cụm (trang \"Cụm server\"), so với \"Mã khóa\" ở trên.${C_0}" ;;
	esac
}

do_cluster_init() {
	[[ -x "$SCS_BIN" ]] || { echo "  Chưa cài dịch vụ (chọn mục 3 trước)."; return 0; }
	if [[ "$(cluster_state)" != none ]]; then
		echo "  Server này đã thuộc một cụm hoặc đang xin vào cụm (xem mục 12)."
		return 0
	fi
	echo "  Tạo cụm MỚI với server này là server đầu tiên. Chỉ làm ${C_B}một lần, trên một server${C_0};"
	echo "  các server sau dùng mục 14 để xin vào cụm này."
	ask "Tên cụm (chữ thường, số, . _ -)" "scs"
	local cname="$REPLY"
	valid_node_name "$cname" || { echo "  ${C_RED}Tên cụm không hợp lệ${C_0}"; return 1; }
	ask "Tên server này trong cụm (ví dụ hn-01)" "$(default_node_name)"
	local name="$REPLY"
	valid_node_name "$name" || { echo "  ${C_RED}Tên server không hợp lệ (chữ thường, số, . _ -, tối đa 32 ký tự)${C_0}"; return 1; }
	ask_cluster_addrs || return 1
	echo
	confirm "Tạo cụm \"$cname\" với server \"$name\"?" || return 0
	cluster_cmd init -cluster "$cname" -name "$name" "${ADDR_ARGS[@]}" || return 1
	cluster_after
	echo
	echo "  Đưa dòng ${C_B}Mã khóa${C_0} ở trên cho người cài server tiếp theo (họ nhập ở mục 14)."
}

do_cluster_join() {
	[[ -x "$SCS_BIN" ]] || { echo "  Chưa cài dịch vụ (chọn mục 3 trước)."; return 0; }
	if [[ "$(cluster_state)" != none ]]; then
		echo "  Server này đã thuộc một cụm hoặc đang xin vào cụm (xem mục 12)."
		return 0
	fi
	echo "  Xin vào một cụm đã có. Cần hai thứ lấy từ một server đang ở trong cụm (mục 12 trên server đó):"
	echo "    - địa chỉ cổng cụm của nó, ví dụ https://192.168.1.10:5222"
	echo "    - dòng \"Mã khóa\" của nó (để chắc là gọi đúng server, không bị kẻ khác chen giữa)"
	ask "Địa chỉ cổng cụm của server trong cụm" ""
	local target="${REPLY%/}"
	valid_cluster_url "$target" || { echo "  ${C_RED}Phải có dạng https://<IP hoặc tên miền>:<cổng>${C_0}"; return 1; }
	ask "Mã khóa của server đó" ""
	local fp="$REPLY"
	[[ "$fp" =~ ^[0-9a-fA-F]{4}(-[0-9a-fA-F]{4}){7}$ ]] || { echo "  ${C_RED}Mã khóa có dạng xxxx-xxxx-xxxx-xxxx-xxxx-xxxx-xxxx-xxxx${C_0}"; return 1; }
	ask "Tên server này trong cụm (ví dụ hn-02)" "$(default_node_name)"
	local name="$REPLY"
	valid_node_name "$name" || { echo "  ${C_RED}Tên server không hợp lệ (chữ thường, số, . _ -, tối đa 32 ký tự)${C_0}"; return 1; }
	ask_cluster_addrs || return 1
	echo
	cluster_cmd join "$target" -fingerprint "$fp" -name "$name" "${ADDR_ARGS[@]}" || return 1
	cluster_after
	echo
	echo "  Bây giờ admin đăng nhập web của ${C_B}từng${C_0} server trong cụm → trang \"Cụm server\" → so Mã khóa ở trên → Duyệt."
	echo "  Đủ mọi server duyệt thì server này tự vào cụm (kiểm bằng mục 12)."
}

# ---- mục 16: kho bộ rule — thêm file rule, tự chia chưa ký / đã ký / lỗi, duyệt (ký), xuất bản đã ký
rules_cmd() {
	[[ -x "$SCS_BIN" ]] || { echo "  Chưa cài dịch vụ (chọn mục 3 trước)."; return 1; }
	# Chạy từ "/" (xem cluster_cmd); thư mục kho, khóa phát hành, thư mục cụm lấy từ server.env.
	(cd / && sudo -u scs "$SCS_BIN" rules "$@" -env-file "$ENV_FILE")
}

do_rules() {
	local c f name
	while true; do
		echo
		rules_cmd list || return 0
		echo
		echo "  ${C_DIM}Rule chỉ đọc log và khớp regex, không chạy được lệnh trên máy. Duyệt = ký bằng khóa phát hành${C_0}"
		echo "  ${C_DIM}của server (server trong cụm: duyệt bộ mới trên web để các server khác cùng ký). Sửa rule đã ký${C_0}"
		echo "  ${C_DIM}thì tăng version rồi thêm lại. Web quản trị: trang \"Rule quét log\".${C_0}"
		echo
		echo "   a) Thêm file rule   d) Duyệt và ký   x) Xuất bộ đã ký   r) Xóa   0) Quay lại"
		read -r -p "  Chọn: " c
		case "$c" in
			a | A)
				ask "Đường dẫn file rule .yaml (ví dụ ~/scs-rules/bo-moi.yaml)" ""
				f="${REPLY/#\~/$HOME}"
				[[ -n "$f" ]] || continue
				[[ -f "$f" && -r "$f" ]] || { echo "  ${C_RED}Không đọc được file $f${C_0}"; continue; }
				rules_cmd add - <"$f"
				;;
			d | D)
				ask "Tên file cần duyệt (cột File ở trên, ví dụ bo-moi@2026.11.20-01.yaml)" ""
				f="$REPLY"
				[[ -n "$f" ]] || continue
				echo "  Bộ rule sau khi ký sẽ được dùng cho agent. Kiểm tra kỹ nội dung trước khi duyệt."
				confirm "Duyệt và ký $f?" && rules_cmd approve "$f"
				;;
			x | X)
				ask "Bộ đã ký cần xuất (TÊN@PHIÊN_BẢN, ví dụ bo-moi@2026.11.20-01)" ""
				name="$REPLY"
				[[ -n "$name" && "$name" != */* ]] || continue
				f="$HOME/$name.json"
				if rules_cmd export "$name" >"$f.tmp"; then
					mv "$f.tmp" "$f"
					echo "  ${C_GRN}✔${C_0} Đã ghi $f"
					echo "  Chép sang máy agent:  scp $f <user>@<IP máy>:/tmp/"
					echo "  Trên máy agent:       sudo scs-agent rules -install /tmp/$name.json"
				else
					rm -f "$f.tmp"
				fi
				;;
			r | R)
				ask "Tên file cần xóa (cột File)" ""
				f="$REPLY"
				[[ -n "$f" ]] || continue
				confirm "Xóa file nguồn $f?" || continue
				if confirm "Xóa luôn bản đã ký của bộ này (agent đã cài không bị ảnh hưởng)?"; then
					rules_cmd remove "$f" -bundle
				else
					rules_cmd remove "$f"
				fi
				;;
			0 | q | Q | "") return 0 ;;
			*) echo "  Không có mục \"$c\"." ;;
		esac
	done
}

# ---------------------------------------------------------------- menu
while true; do
	clear 2>/dev/null || true
	printf '%s SCS-Server — menu cài đặt và vận hành%s  %s(menu %s)%s\n\n' "$C_B" "$C_0" "$C_DIM" "$MENU_VER" "$C_0"
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
   7) Đổi IP LAN / thêm IP vào chứng chỉ server (cấp lại chứng chỉ, ghi danh sách cho agent)

  ${C_B}Vận hành${C_0}
   8) Lấy mã đăng nhập web
   9) Trạng thái và log gần nhất
  10) Gỡ dịch vụ (giữ dữ liệu)

  ${C_B}Agent${C_0}
  11) Đóng gói agent để cài cho máy tại điểm bán (một file .tar.gz)

  ${C_B}Cụm nhiều server${C_0}
  12) Trạng thái cụm, mã khóa của server này
  13) Tạo cụm mới (server đầu tiên)
  14) Xin vào cụm đã có

  ${C_B}Địa chỉ cho agent${C_0}
  15) Thêm / gỡ địa chỉ server gửi cho agent (đổi địa điểm, đổi mạng; không khởi động lại)

  ${C_B}Rule quét log${C_0}
  16) Kho bộ rule: thêm, xem chưa ký / đã ký / lỗi, duyệt (ký), xuất cho agent

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
		12) do_cluster_status ;;
		13) do_cluster_init ;;
		14) do_cluster_join ;;
		15) do_servers ;;
		16) do_rules ;;
		0 | q | Q | "") exit 0 ;;
		*) echo "  Không có mục \"$choice\"." ;;
	esac
	pause
done
