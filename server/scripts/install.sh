#!/usr/bin/env bash
# install.sh - cài SCS-Server thành dịch vụ systemd trên máy server (tự chạy khi khởi động lại máy).
#
# Cách dùng (cần sudo; chạy scripts/setup.sh trước để có Docker, Postgres, NATS và .env):
#   sudo bash scripts/install.sh --wan-ip <IP WAN> [--vpn-ip <IP VPN>]   cài lần đầu, tạo luôn CA + chứng chỉ
#   sudo bash scripts/install.sh                                         cập nhật binary, khởi động lại dịch vụ
#   sudo bash scripts/install.sh --uninstall                             gỡ dịch vụ (GIỮ cấu hình, CA và dữ liệu)
#
# Tùy chọn:
#   --wan-ip IP          IP WAN của server mà agent gọi tới (ghi vào chứng chỉ server), lặp lại được
#   --vpn-ip IP          IP mà admin dùng để vào web quản trị qua VPN, lặp lại được
#   --lan-ip IP          IP LAN của server (máy trong mạng nội bộ vào web bằng IP này), lặp lại được
#   --dns TÊN            tên miền ghi thêm vào chứng chỉ server (tùy chọn)
#   --admin-allow DẢI    cho dải IP này vào web quản trị 7222 (ví dụ 192.168.1.0/24), lặp lại được;
#                        thêm vào SCS_ADMIN_ALLOW (giữ localhost) và mở ufw cho đúng dải đó nếu ufw đang bật;
#                        "private" = toàn mạng nội bộ (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
#   --agent-servers DS   các địa chỉ agent dùng gọi server, cách nhau dấu phẩy, thứ tự ưu tiên
#                        (ví dụ https://192.168.1.10:6222,https://ten.ddns.net:16222); server gửi danh
#                        sách này cho agent đã duyệt, agent tự chuyển khi địa chỉ đang dùng mất kết nối
#   --reissue-cert       cấp lại chứng chỉ server theo các --wan-ip/--vpn-ip/--lan-ip (CA giữ nguyên,
#                        agent không bị ảnh hưởng); dùng khi đổi IP hoặc thêm IP LAN
#
# Script làm:
#   - tạo user hệ thống scs (không đăng nhập được)
#   - chép bin/scs-server → /opt/scs-server/ (chưa có thì build bằng go)
#   - lần đầu: chép server/.env → /etc/scs-server/server.env (scs, 600); đã có thì GIỮ NGUYÊN
#   - chưa có CA: tạo CA nội bộ + chứng chỉ server vào /etc/scs-server/pki và điền SCS_TLS_*, SCS_CA_*
#   - cài deploy/systemd/scs-server.service, bật tự chạy khi khởi động, khởi động ngay
#   - cài lệnh scs-login-code để lấy mã đăng nhập web
# Chạy lại nhiều lần được.

# Chạy bằng sh (dash) thì tự chuyển sang bash: script dùng cú pháp của bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

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
PKI_DIR=$ETC_DIR/pki
PKI_IPS=() PKI_DNS=() ADMIN_ALLOW=() MODE=install REISSUE=0 AGENT_SERVERS="-"

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
trap 'die "Lệnh ở dòng $LINENO thất bại: $BASH_COMMAND" "Xem thông báo phía trên"' ERR

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

while [[ $# -gt 0 ]]; do
	case "$1" in
		--wan-ip | --vpn-ip | --lan-ip) PKI_IPS+=("${2:-}"); shift 2 ;;
		--dns) PKI_DNS+=("${2:-}"); shift 2 ;;
		--admin-allow) ADMIN_ALLOW+=("${2:-}"); shift 2 ;;
		--agent-servers) AGENT_SERVERS="${2:-}"; shift 2 ;;
		--reissue-cert) REISSUE=1; shift ;;
		--uninstall) MODE=uninstall; shift ;;
		-h | --help) sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Tham số không hợp lệ: $1" "Xem: bash $0 --help" ;;
	esac
done
for ip in "${PKI_IPS[@]}"; do
	[[ "$ip" =~ ^[0-9]+([.][0-9]+){3}$ || "$ip" =~ ^[0-9a-fA-F]*:[0-9a-fA-F:]*$ ]] || die "\"$ip\" không phải địa chỉ IP"
done
# "private" = toàn mạng nội bộ: mọi dải IP riêng (công ty nhiều lớp mạng / VLAN định tuyến qua nhau).
expanded=()
for cidr in "${ADMIN_ALLOW[@]}"; do
	if [[ "$cidr" == private ]]; then expanded+=(10.0.0.0/8 172.16.0.0/12 192.168.0.0/16); else expanded+=("$cidr"); fi
done
ADMIN_ALLOW=("${expanded[@]}")
for cidr in "${ADMIN_ALLOW[@]}"; do
	[[ "$cidr" =~ ^[0-9]+([.][0-9]+){3}(/[0-9]{1,2})?$ || "$cidr" =~ ^[0-9a-fA-F]*:[0-9a-fA-F:]*(/[0-9]{1,3})?$ ]] ||
		die "--admin-allow \"$cidr\" không phải IP hoặc dải CIDR (ví dụ 192.168.1.0/24)"
	[[ "$cidr" == 0.0.0.0/0 || "$cidr" == ::/0 ]] &&
		die "--admin-allow $cidr mở web quản trị cho cả Internet" "Chỉ cho dải LAN hoặc VPN, ví dụ 192.168.1.0/24"
done
for d in "${PKI_DNS[@]}"; do
	[[ "$d" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || die "--dns \"$d\" không phải tên miền"
done
if [[ "$AGENT_SERVERS" != "-" ]]; then
	for u in ${AGENT_SERVERS//,/ }; do
		[[ "$u" =~ ^https://[^/:@[:space:]]+:[0-9]{1,5}/?$ ]] || die "--agent-servers: \"$u\" không có dạng https://<IP>:<cổng>"
	done
fi
[[ $REISSUE -eq 0 || ${#PKI_IPS[@]} -gt 0 || ${#PKI_DNS[@]} -gt 0 ]] || die "--reissue-cert cần ít nhất một --wan-ip / --vpn-ip / --lan-ip / --dns"

if [[ $MODE == uninstall ]]; then
	systemctl disable --now scs-server 2>/dev/null || true
	rm -f "$UNIT_DST" "$LOGIN_CMD"
	systemctl daemon-reload
	ok "Đã gỡ dịch vụ scs-server. Giữ lại: $ETC_DIR (cấu hình, CA), $DATA_DIR (dữ liệu), $OPT_DIR (binary), user $SVC_USER"
	exit 0
fi

# ---- binary
if [[ ! -x "$BIN_SRC" && -x "$OPT_DIR/scs-server" && (${#ADMIN_ALLOW[@]} -gt 0 || $REISSUE -eq 1) ]]; then
	# Chỉ thêm dải admin / cấp lại chứng chỉ: dùng luôn binary đang chạy, không build lại.
	BIN_SRC="$OPT_DIR/scs-server"
elif [[ ! -x "$BIN_SRC" ]]; then
	GO="$(command -v go || echo /usr/local/go/bin/go)"
	[[ -x "$GO" ]] || die "Chưa có $BIN_SRC và không có go để build" "Chạy scripts/setup.sh trước, hoặc build trên máy khác rồi chép vào bin/"
	echo "Build scs-server..."
	# -buildvcs=false: chạy bằng root trên repo của user khác thì git từ chối ("dubious ownership").
	repo="$(cd "$SERVER_DIR/.." && pwd)"
	build_ver="$(git -c safe.directory="$repo" -C "$repo" describe --tags --always 2>/dev/null || echo dev)"
	(cd "$SERVER_DIR" && CGO_ENABLED=0 "$GO" build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$build_ver" -o "$BIN_SRC" ./cmd/scs-server) ||
		die "Build scs-server thất bại" "Xem lỗi go phía trên; hoặc chạy menu mục 4 (cập nhật) để build bằng user thường"
fi
"$BIN_SRC" version >/dev/null || die "$BIN_SRC không chạy được trên máy này" "Build lại cho đúng kiến trúc (GOARCH)"

# ---- bản agent đi kèm: server phát hành cho các agent đã duyệt (trang "Agent" trên web)
AGENT_BIN="$SERVER_DIR/bin/agent"
srv_ver="$("$BIN_SRC" version)"
if [[ "$BIN_SRC" != "$OPT_DIR/scs-server" && "$(cat "$AGENT_BIN/VERSION" 2>/dev/null)" != "$srv_ver" && -d "$SERVER_DIR/../agent" ]]; then
	if bash "$SERVER_DIR/scripts/build-agent.sh" "$srv_ver"; then
		# Build bằng root trong thư mục code của user khác: trả quyền để lần cập nhật sau ghi đè được.
		chown -R --reference="$SERVER_DIR" "$SERVER_DIR/bin"
	else
		warn "Không build được bản agent đi kèm: server chạy bình thường nhưng chưa phát hành được agent"
	fi
fi

# ---- user, thư mục
if ! id "$SVC_USER" >/dev/null 2>&1; then
	useradd --system --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
	ok "Đã tạo user hệ thống $SVC_USER"
fi
install -d -o root -g root -m 755 "$OPT_DIR" "$ETC_DIR"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 700 "$DATA_DIR"
# Giữ bản đang chạy làm scs-server.prev để quay về được (update.sh --rollback).
if [[ -x "$OPT_DIR/scs-server" ]] && ! cmp -s "$BIN_SRC" "$OPT_DIR/scs-server"; then
	cp -p "$OPT_DIR/scs-server" "$OPT_DIR/scs-server.prev"
fi
install -o root -g root -m 755 "$BIN_SRC" "$OPT_DIR/scs-server.new"
mv -f "$OPT_DIR/scs-server.new" "$OPT_DIR/scs-server"
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
# ---- CA nội bộ + chứng chỉ server (agent gọi thẳng bằng IP WAN nên chứng chỉ phải ghi IP đó)
set_env() { # set_env KEY VALUE: thay dòng KEY=... hoặc thêm vào cuối file
	if grep -qE "^$1=" "$ENV_FILE"; then
		sed -i -E "s|^$1=.*|$1=$2|" "$ENV_FILE"
	else
		printf '%s=%s\n' "$1" "$2" >>"$ENV_FILE"
	fi
}
# Postgres/NATS chạy bằng compose.dev.yml trên chính máy này lấy mật khẩu từ server/.env.
# Bản sao trong $ENV_FILE lệch (tạo lại .env, xóa volume DB) thì scs-server bị từ chối đăng nhập:
# chép mật khẩu từ server/.env sang. DB ở máy khác (SCS_DB_HOST không phải máy này) thì không đụng.
if [[ -f "$SERVER_DIR/.env" ]] && [[ "$(env_value "$ENV_FILE" SCS_DB_HOST)" =~ ^(127\.0\.0\.1|localhost|::1)?$ ]]; then
	synced=()
	for key in SCS_DB_PASSWORD SCS_NATS_PASSWORD; do
		val="$(env_value "$SERVER_DIR/.env" "$key")"
		[[ -n "$val" && "$val" != "$(env_value "$ENV_FILE" "$key")" ]] || continue
		[[ ${#synced[@]} -eq 0 ]] && cp -p "$ENV_FILE" "$ENV_FILE.bak-$(date +%Y%m%d-%H%M%S)"
		set_env "$key" "$val"
		synced+=("$key")
	done
	[[ ${#synced[@]} -eq 0 ]] || ok "Đã chép ${synced[*]} từ server/.env sang $ENV_FILE cho khớp Postgres/NATS (bản cũ: $ENV_FILE.bak-*)"
fi
if [[ -n "$(env_value "$ENV_FILE" SCS_CA_CERT)" ]]; then
	ok "Đã có CA: $(env_value "$ENV_FILE" SCS_CA_CERT)"
	# Cài lại kèm địa chỉ chưa có trong chứng chỉ server: tự cấp lại, GIỮ các địa chỉ đang có.
	if [[ $REISSUE -eq 0 && ${#PKI_IPS[@]}${#PKI_DNS[@]} != 00 && -f "$PKI_DIR/server.pem" ]] && command -v openssl >/dev/null; then
		san="$(openssl x509 -in "$PKI_DIR/server.pem" -noout -ext subjectAltName 2>/dev/null | grep -oE '(IP Address|DNS):[^, ]+' || true)"
		missing=0
		for ip in "${PKI_IPS[@]}"; do grep -qxF "IP Address:$ip" <<<"$san" || missing=1; done
		for d in "${PKI_DNS[@]}"; do grep -qxF "DNS:$d" <<<"$san" || missing=1; done
		if [[ $missing -eq 1 ]]; then
			while IFS= read -r x; do
				case "$x" in
					"IP Address:127.0.0.1") ;;
					"IP Address:"*) [[ " ${PKI_IPS[*]} " == *" ${x#IP Address:} "* ]] || PKI_IPS+=("${x#IP Address:}") ;;
					DNS:*) [[ " ${PKI_DNS[*]} " == *" ${x#DNS:} "* ]] || PKI_DNS+=("${x#DNS:}") ;;
				esac
			done <<<"$san"
			REISSUE=1
			ok "Địa chỉ mới chưa có trong chứng chỉ server: cấp lại (giữ các địa chỉ đang có)"
		fi
	fi
	if [[ $REISSUE -eq 1 ]]; then
		# Cấp lại chứng chỉ server từ CA đang có: agent vẫn tin (cùng CA), server tự nạp file mới.
		stamp="$(date +%Y%m%d-%H%M%S)"
		for f in server.pem server.key; do
			[[ -f "$PKI_DIR/$f" ]] && mv -f "$PKI_DIR/$f" "$PKI_DIR/$f.old-$stamp"
		done
		args=(-ip 127.0.0.1)
		for ip in "${PKI_IPS[@]}"; do args+=(-ip "$ip"); done
		for d in "${PKI_DNS[@]}"; do args+=(-dns "$d"); done
		if ! "$OPT_DIR/scs-server" pki server "${args[@]}" -dir "$PKI_DIR" >/dev/null; then
			for f in server.pem server.key; do
				[[ -f "$PKI_DIR/$f.old-$stamp" ]] && mv -f "$PKI_DIR/$f.old-$stamp" "$PKI_DIR/$f"
			done
			die "Không cấp lại được chứng chỉ server (đã trả lại chứng chỉ cũ)"
		fi
		chown -R "$SVC_USER:$SVC_USER" "$PKI_DIR"
		ok "Đã cấp lại chứng chỉ server cho 127.0.0.1 ${PKI_IPS[*]} ${PKI_DNS[*]} (bản cũ: *.old-$stamp)"
	elif [[ ${#PKI_IPS[@]} -gt 0 ]]; then
		ok "Chứng chỉ server đã có đủ các địa chỉ vừa nhập"
	fi
elif [[ ${#PKI_IPS[@]} -eq 0 && ${#PKI_DNS[@]} -eq 0 ]]; then
	warn "Chưa có CA và chứng chỉ server: agent chưa kết nối được. Chạy lại: sudo bash $0 --wan-ip <IP WAN của server> [--vpn-ip <IP VPN>]"
else
	args=(-ip 127.0.0.1) # để kiểm tra được ngay trên máy server
	for ip in "${PKI_IPS[@]}"; do args+=(-ip "$ip"); done
	for d in "${PKI_DNS[@]}"; do args+=(-dns "$d"); done
	"$OPT_DIR/scs-server" pki init "${args[@]}" -dir "$PKI_DIR" >/dev/null ||
		die "Không tạo được CA" "Nếu $PKI_DIR đã có file từ lần chạy trước: kiểm tra rồi xóa thư mục đó và chạy lại"
	chown -R "$SVC_USER:$SVC_USER" "$PKI_DIR"
	chmod 700 "$PKI_DIR"
	set_env SCS_TLS_CERT "$PKI_DIR/server.pem"
	set_env SCS_TLS_KEY "$PKI_DIR/server.key"
	set_env SCS_TLS_CLIENT_CA "$PKI_DIR/ca.pem"
	set_env SCS_CA_CERT "$PKI_DIR/ca.pem"
	set_env SCS_CA_KEY "$PKI_DIR/ca.key"
	ok "Đã tạo CA và chứng chỉ server cho ${PKI_IPS[*]} (thư mục $PKI_DIR, khóa CA quyền 600)"
fi
# Khóa phát hành: ký các bản agent server phân phối; khóa công khai đóng vào gói cài agent.
if [[ -d "$PKI_DIR" ]]; then
	if [[ ! -f "$PKI_DIR/release.key" ]]; then
		"$OPT_DIR/scs-server" pki release -dir "$PKI_DIR" >/dev/null || die "Không tạo được khóa phát hành"
		chown "$SVC_USER:$SVC_USER" "$PKI_DIR/release.key" "$PKI_DIR/release.pub"
		ok "Đã tạo khóa phát hành bản agent: $PKI_DIR/release.key"
	fi
	set_env SCS_RELEASE_KEY "$PKI_DIR/release.key"
	install -o root -g root -m 644 "$PKI_DIR/release.pub" "$ETC_DIR/agent-release.pub"
fi
[[ -n "$(env_value "$ENV_FILE" SCS_AGENT_DIST)" ]] || set_env SCS_AGENT_DIST "$OPT_DIR/agent"
[[ -n "$(env_value "$ENV_FILE" SCS_AGENT_RELEASES)" ]] || set_env SCS_AGENT_RELEASES "$DATA_DIR/agent-releases"
# Thư mục cụm luôn ghi đường dẫn tuyệt đối: lệnh "scs-server cluster ..." gõ tay từ thư mục khác
# vẫn dùng đúng chỗ dịch vụ đang dùng (giá trị tương đối tính từ thư mục làm việc của dịch vụ).
cluster_dir_cfg="$(env_value "$ENV_FILE" SCS_CLUSTER_DIR)"
cluster_dir_cfg="${cluster_dir_cfg:-data/cluster}"
[[ "$cluster_dir_cfg" == /* ]] || set_env SCS_CLUSTER_DIR "$DATA_DIR/$cluster_dir_cfg"
if [[ -f "$AGENT_BIN/VERSION" ]]; then
	install -d -o root -g root -m 755 "$OPT_DIR/agent"
	rm -f "$OPT_DIR/agent"/scs-agent-linux-* "$OPT_DIR/agent/VERSION"
	install -o root -g root -m 644 "$AGENT_BIN"/VERSION "$AGENT_BIN"/scs-agent-linux-* "$OPT_DIR/agent/"
	ok "Bản agent đi kèm: $(cat "$AGENT_BIN/VERSION") (server nhập vào kho phát hành khi khởi động)"
fi

# Bản CA để mang sang máy tại điểm bán (chỉ chứng chỉ, không phải khóa).
CA_PEM="$(env_value "$ENV_FILE" SCS_CA_CERT)"
if [[ -n "$CA_PEM" && -r "$CA_PEM" ]]; then
	install -o root -g root -m 644 "$CA_PEM" "$ETC_DIR/agent-ca.pem"
fi

if [[ "$AGENT_SERVERS" != "-" ]]; then
	set_env SCS_AGENT_SERVERS "$AGENT_SERVERS"
	ok "Địa chỉ gửi cho agent: ${AGENT_SERVERS:-(chỉ địa chỉ lúc cài)}"
fi

# ---- ai được vào web quản trị (7222)
if [[ ${#ADMIN_ALLOW[@]} -gt 0 ]]; then
	allow="$(env_value "$ENV_FILE" SCS_ADMIN_ALLOW)"
	allow="${allow:-127.0.0.0/8,::1}"
	for cidr in "${ADMIN_ALLOW[@]}"; do
		[[ ",$allow," == *",$cidr,"* ]] || allow="$allow,$cidr"
	done
	set_env SCS_ADMIN_ALLOW "$allow"
	ok "Web quản trị nhận kết nối từ: $allow"
	admin_listen="$(env_value "$ENV_FILE" SCS_ADMIN_LISTEN)"
	admin_port="${admin_listen##*:}"
	admin_port="${admin_port:-7222}"
	if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
		for cidr in "${ADMIN_ALLOW[@]}"; do
			ufw allow from "$cidr" to any port "$admin_port" proto tcp comment "SCS web quan tri" >/dev/null
		done
		ok "ufw: đã mở cổng $admin_port cho ${ADMIN_ALLOW[*]}"
	fi
fi

# Cổng agent (6222) phải mở cho mọi nơi agent gọi tới (LAN, VLAN khác, Internet qua router):
# bảo vệ bằng TLS, chứng chỉ máy và admin duyệt, không bằng tường lửa.
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
	ingest_listen="$(env_value "$ENV_FILE" SCS_INGEST_LISTEN)"
	ingest_port="${ingest_listen##*:}"
	ingest_port="${ingest_port:-6222}"
	ufw allow "$ingest_port/tcp" comment "SCS agent" >/dev/null
	ok "ufw: đã mở cổng agent $ingest_port"
	# Cổng cụm (5222): chỉ mở khi server đã thuộc một cụm hoặc đang xin vào cụm. Cổng này chỉ nhận
	# server có khóa trong danh sách cụm (và yêu cầu xin vào cụm, phải được admin duyệt).
	cluster_dir="$(env_value "$ENV_FILE" SCS_CLUSTER_DIR)"
	cluster_dir="${cluster_dir:-data/cluster}"
	[[ "$cluster_dir" == /* ]] || cluster_dir="$DATA_DIR/$cluster_dir"
	if [[ -f "$cluster_dir/log.jsonl" || -f "$cluster_dir/join.json" ]]; then
		cluster_listen="$(env_value "$ENV_FILE" SCS_CLUSTER_LISTEN)"
		cluster_port="${cluster_listen##*:}"
		cluster_port="${cluster_port:-5222}"
		ufw allow "$cluster_port/tcp" comment "SCS cum server" >/dev/null
		ok "ufw: đã mở cổng cụm $cluster_port"
	fi
fi

[[ "$(env_value "$ENV_FILE" SCS_ENV)" == prod ]] ||
	warn "SCS_ENV trong $ENV_FILE chưa phải prod (đang chạy thử thì bỏ qua)"
# Thư mục dữ liệu đang ghi tương đối (mặc định ./data/devices, tính từ $DATA_DIR): đổi sang đường dẫn
# tuyệt đối trỏ đúng chỗ đó, dữ liệu không phải chuyển.
storage="$(env_value "$ENV_FILE" SCS_STORAGE_ROOT)"
storage="${storage:-./data/devices}"
if [[ "$storage" != /* ]]; then
	storage="$DATA_DIR/${storage#./}"
	set_env SCS_STORAGE_ROOT "$storage"
	ok "SCS_STORAGE_ROOT=$storage"
fi
for key in SCS_TLS_CERT SCS_TLS_KEY SCS_TLS_CLIENT_CA SCS_CA_CERT SCS_CA_KEY SCS_STORAGE_ROOT; do
	val="$(env_value "$ENV_FILE" "$key")"
	if [[ -n "$val" && "$val" != /* ]]; then
		warn "$key=$val là đường dẫn tương đối, sẽ tính từ $DATA_DIR; nên dùng đường dẫn tuyệt đối"
	fi
	if [[ "$key" != SCS_STORAGE_ROOT && -n "$val" && "$val" == /* ]] && ! sudo -u "$SVC_USER" test -r "$val"; then
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
if [[ -r "$ETC_DIR/agent-ca.pem" ]] && command -v curl >/dev/null; then
	listen="$(env_value "$ENV_FILE" SCS_INGEST_LISTEN)"
	port="${listen##*:}"
	if curl -sS --max-time 5 --cacert "$ETC_DIR/agent-ca.pem" "https://127.0.0.1:${port:-6222}/healthz" | grep -q '"status":"ok"'; then
		ok "Cổng agent ${port:-6222} trả lời qua HTTPS, chứng chỉ khớp CA"
	else
		warn "Chưa kiểm được cổng agent qua HTTPS (xem: journalctl -u scs-server -n 50)"
	fi
fi
cat <<EOF

  Cài agent trên từng máy tại điểm bán:
    1. Menu server mục 11 (bash server/menu.sh) đóng gói agent thành một file .tar.gz.
    2. Chép file đó sang máy, giải nén, chạy: sudo bash scs-agent/scripts/install.sh
    3. Vào web quản trị, trang "Chờ duyệt", đối chiếu mã khóa rồi bấm Duyệt.

  Xem log:          journalctl -u scs-server -f
  Trạng thái:       systemctl status scs-server
  Lấy mã đăng nhập: scs-login-code
  Sửa cấu hình:     sudo nano $ENV_FILE && sudo systemctl restart scs-server
  Cập nhật:         bash server/menu.sh, mục 4
EOF
