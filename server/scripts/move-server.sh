#!/usr/bin/env bash
# move-server.sh - chép NGUYÊN SCS-Server (DB, CA, khóa phát hành, cấu hình, dữ liệu) sang một server khác
# QUA SSH, giữ đúng CA và khóa phát hành nên agent đang chạy không phải cài lại (menu mục 20).
#
#   Cách 1 — máy cũ SSH được sang máy mới (máy cũ ĐẨY):
#     Máy CŨ:  sudo bash scripts/move-server.sh export --to <user>@<máy mới> [--port 22]
#   Cách 2 — máy mới không nhận SSH vào (sau NAT / tường lửa công ty), máy mới KÉO về:
#     Máy CŨ:  sudo bash scripts/move-server.sh export --pull         (để gói trong ~ của user chạy sudo, quyền 600)
#     Máy MỚI: sudo bash scripts/move-server.sh fetch --from <user>@<máy cũ> [--port 22]
#              (scp gói về, kiểm SHA-256, rồi XÓA gói trên máy cũ bằng shred)
#   Rồi máy MỚI: sudo bash scripts/move-server.sh import --bundle ~/scs-move-<giờ>.tar.gz [--wan-ip IP] [--lan-ip IP]
#                                                    [--dns TÊN] [--vpn-ip IP]   (chuyển cho install.sh)
#   Máy CŨ, nếu gói kéo về rồi mà chưa bị xóa: sudo bash scripts/move-server.sh clean
#   Tùy chọn chung: --yes (không hỏi)
#
# Trước khi export: máy mới đã chạy menu mục 1 → 2 (Docker, Go, PostgreSQL), CHƯA chạy mục 3; code máy mới cùng
# hoặc mới hơn máy cũ; địa chỉ của máy mới đã có trong danh sách địa chỉ gửi cho agent (menu mục 15) và gần 100%
# máy đã nhận — agent tự chuyển sang máy mới khi máy cũ dừng.
#
# Bảo vệ dữ liệu:
#   - Gói chứa khóa CA, khóa phát hành, mật khẩu (server.env) và email người dùng: chỉ nằm trong thư mục 700
#     của root, chỉ chuyển bằng scp (SSH), kiểm SHA-256 ở hai đầu, xóa (shred) bản tạm ngay khi xong.
#   - export DỪNG và TẮT TỰ CHẠY scs-server ở máy cũ (khởi động lại máy cũ cũng không chạy lại) để không có hai
#     server cùng nhận dữ liệu. Quay lại máy cũ: sudo systemctl enable --now scs-server.
#   - import từ chối ghi đè server đang có (đã có /etc/scs-server) trừ khi --force (cất bản cũ sang *.before-move-*).
#   - Không gửi gì ra ngoài ngoài máy đích do người chạy chỉ định.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Eeuo pipefail

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$SERVER_DIR/.." && pwd)"
SVC_USER=scs
OPT_DIR=/opt/scs-server
ETC_DIR=/etc/scs-server
DATA_DIR=/var/lib/scs-server
COMPOSE_FILE="$SERVER_DIR/compose.dev.yml"
MANIFEST_VER=1
tmp="" # thư mục tạm chứa bí mật (biến chung để trap EXIT xóa được khi script dừng giữa chừng)

if [[ -t 1 ]]; then
	C_RED=$'\e[31m' C_GRN=$'\e[32m' C_YEL=$'\e[33m' C_B=$'\e[1m' C_0=$'\e[0m'
else
	C_RED='' C_GRN='' C_YEL='' C_B='' C_0=''
fi
step() { printf '\n%s==> %s%s\n' "$C_B" "$*" "$C_0"; }
ok()   { printf '  %s✔%s %s\n' "$C_GRN" "$C_0" "$*"; }
warn() { printf '  %s!%s %s\n' "$C_YEL" "$C_0" "$*"; }
die() {
	printf '\n%s✖ LỖI: %s%s\n' "$C_RED$C_B" "$1" "$C_0" >&2
	[[ $# -gt 1 ]] && printf '  Cách xử lý: %s\n' "$2" >&2
	exit 1
}
trap 'die "Lệnh ở dòng $LINENO thất bại: $BASH_COMMAND" "Xem thông báo phía trên"' ERR

MODE="${1:-}"
if [[ "$MODE" == -h || "$MODE" == --help ]]; then sed -n '2,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0; fi
[[ $# -gt 0 ]] && shift
TO="" FROM="" PORT=22 BUNDLE="" YES=0 FORCE=0 PULL=0
INSTALL_ARGS=()
while [[ $# -gt 0 ]]; do
	case "$1" in
		--to) TO="${2:-}"; shift 2 ;;
		--from) FROM="${2:-}"; shift 2 ;;
		--pull) PULL=1; shift ;;
		--port) PORT="${2:-}"; shift 2 ;;
		--bundle) BUNDLE="${2:-}"; shift 2 ;;
		--wan-ip | --lan-ip | --vpn-ip | --dns) INSTALL_ARGS+=("$1" "${2:-}"); shift 2 ;;
		--force) FORCE=1; shift ;;
		--yes | -y) YES=1; shift ;;
		-h | --help) sed -n '2,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Tham số không hợp lệ: $1" "Xem: bash $0 --help" ;;
	esac
done
[[ "$MODE" =~ ^(export|import|fetch|clean)$ ]] || die "Chọn export / clean (máy cũ) hoặc fetch / import (máy mới)" "Xem: bash $0 --help"
[[ $EUID -eq 0 ]] || die "Cần chạy bằng sudo (đọc / ghi khóa CA, cấu hình, dữ liệu)" "sudo bash $0 $MODE ..."
umask 077

confirm() {
	[[ $YES -eq 1 ]] && return 0
	local a
	read -r -p "  $1 [y/N] " a
	[[ "$a" =~ ^[yY]$ ]]
}

# env_value FILE KEY: đọc một biến trong file .env (không source).
env_value() {
	local line
	line="$(grep -E "^(export[[:space:]]+)?$2=" "$1" 2>/dev/null | tail -n1 || true)"
	line="${line#*=}"
	line="$(sed -E 's/^[[:space:]]*//; s/^#.*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//' <<<"$line")"
	line="${line#\"}"; line="${line%\"}"
	printf '%s' "$line"
}

dc() { docker compose -f "$COMPOSE_FILE" "$@"; }

# wipe: xóa an toàn (shred nếu có) một thư mục / file tạm chứa bí mật.
wipe() {
	local p
	for p in "$@"; do
		[[ -e "$p" ]] || continue
		if command -v shred >/dev/null; then
			find "$p" -type f -exec shred -u -z {} + 2>/dev/null || true
		fi
		rm -rf "$p"
	done
}

# ======================================================================= export (máy cũ)
do_export() {
	if [[ $PULL -eq 1 ]]; then
		[[ -z "$TO" ]] || die "Chọn một: --to (máy cũ đẩy sang) hoặc --pull (máy mới kéo về)"
	else
		[[ -n "$TO" ]] || die "Thiếu --to <user>@<máy mới> (hoặc --pull nếu máy mới không nhận SSH vào)"
		[[ "$TO" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9.:_-]+$ ]] || die "--to phải có dạng user@máy (IP hoặc tên), không có đường dẫn"
	fi
	[[ "$PORT" =~ ^[0-9]{1,5}$ ]] || die "--port không hợp lệ"
	[[ -x "$OPT_DIR/scs-server" && -f "$ETC_DIR/server.env" ]] || die "Máy này chưa cài SCS-Server (không có $OPT_DIR/scs-server, $ETC_DIR/server.env)"
	local runas="${SUDO_USER:-}"
	[[ -n "$runas" && "$runas" != root ]] ||
		die "Chạy bằng sudo từ user thường (khóa SSH tới máy mới là của user đó)" "Đăng nhập user thường rồi: sudo bash $0 export --to ..."
	local version
	version="$("$OPT_DIR/scs-server" version 2>/dev/null || echo "?")"

	step "1. Kiểm tra trước khi chép"
	echo "  Bản server đang chạy: $version"
	local servers
	servers="$(env_value "$ETC_DIR/server.env" SCS_AGENT_SERVERS_FILE)"
	if [[ -r "$servers" ]]; then
		echo "  Danh sách địa chỉ đang gửi cho agent ($servers):"
		grep -oE 'https://[^"]+' "$servers" | sed 's/^/    /' || true
	fi
	warn "Địa chỉ của máy mới PHẢI có trong danh sách trên (menu mục 15) và gần 100% máy đã nhận — nếu không, agent không tìm được máy mới"
	if [[ $PULL -eq 0 ]]; then
		echo "  ssh tới máy mới bằng user $runas (khóa SSH của user này):"
		sudo -u "$runas" ssh -p "$PORT" -o BatchMode=yes -o ConnectTimeout=10 "$TO" true ||
			die "Không SSH được tới $TO bằng khóa của $runas" "Kiểm tra: ssh -p $PORT $TO (dùng khóa SSH); máy mới không nhận SSH vào thì dùng --pull"
		ok "SSH tới $TO được"
	else
		warn "Chế độ kéo: gói sẽ nằm ở /home của $runas (quyền 600) tới khi máy mới chạy fetch — fetch xóa gói sau khi kéo về"
	fi
	echo
	warn "Tiếp theo: DỪNG và TẮT TỰ CHẠY scs-server ở máy này (agent giữ sự kiện trong hàng đợi, tự chuyển sang máy mới)."
	confirm "Dừng server này và đóng gói toàn bộ?" || exit 0

	step "2. Dừng server, gom dữ liệu"
	systemctl disable --now scs-server >/dev/null 2>&1 || systemctl stop scs-server
	ok "Đã dừng và tắt tự chạy scs-server (quay lại: sudo systemctl enable --now scs-server)"
	local stamp name
	tmp="$(mktemp -d /root/scs-move.XXXXXX)"
	chmod 700 "$tmp"
	trap '[[ -n "$tmp" ]] && wipe "$tmp"' EXIT
	stamp="$(date +%Y%m%d-%H%M%S)"
	name="scs-move-$stamp.tar.gz"
	mkdir -p "$tmp/pack/db"
	bash "$SERVER_DIR/scripts/backup-db.sh" --dir "$tmp/pack/db" --reason chuyen-server >/dev/null ||
		die "Sao lưu DB thất bại, KHÔNG chép" "Server đang dừng: sudo systemctl enable --now scs-server để chạy lại"
	local dump
	dump="$(find "$tmp/pack/db" -name '*.sql.gz' -type f | head -n1)"
	[[ -s "$dump" ]] || die "Không thấy bản sao lưu DB"
	mv "$dump" "$tmp/pack/db/scs.sql.gz"
	{
		echo "manifest=$MANIFEST_VER"
		echo "version=$version"
		echo "commit=$(git -C "$REPO_DIR" rev-parse "${version%-dirty}^{commit}" 2>/dev/null || echo "")"
		echo "created=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		echo "db_user=$(env_value "$ETC_DIR/server.env" SCS_DB_USER)"
		echo "db_name=$(env_value "$ETC_DIR/server.env" SCS_DB_NAME)"
	} >"$tmp/pack/MANIFEST"
	# Giữ chủ sở hữu theo TÊN (scs, root): máy mới tạo user scs trước khi giải nén.
	tar -C / -cpf "$tmp/pack/files.tar" etc/scs-server var/lib/scs-server
	tar -C "$tmp/pack" -czf "$tmp/$name" MANIFEST db/scs.sql.gz files.tar
	wipe "$tmp/pack"
	(cd "$tmp" && sha256sum "$name" >"$name.sha256")
	ok "Gói: $name ($(du -h "$tmp/$name" | cut -f1)), SHA-256 $(cut -c1-16 "$tmp/$name.sha256")…"

	if [[ $PULL -eq 1 ]]; then
		step "3. Để gói cho máy mới kéo về"
		local home
		home="$(getent passwd "$runas" | cut -d: -f6)"
		[[ -d "$home" ]] || die "Không thấy thư mục nhà của $runas"
		install -o "$runas" -g "$(id -gn "$runas")" -m 600 "$tmp/$name" "$tmp/$name.sha256" "$home/"
		wipe "$tmp"
		trap - EXIT
		ok "Gói ở $home/$name (chỉ $runas đọc được)"
		echo
		echo "  Trên máy mới chạy (máy mới SSH sang máy này bằng user $runas):"
		echo "    sudo bash scripts/move-server.sh fetch --from $runas@<IP hoặc tên máy này> [--port <cổng SSH>]"
		echo "  fetch kéo gói về, kiểm SHA-256 rồi XÓA gói ở máy này. Chưa fetch được thì xóa tay: sudo bash $0 clean"
		return 0
	fi
	step "3. Chép sang $TO qua SSH"
	chown "$runas" "$tmp" "$tmp/$name" "$tmp/$name.sha256"
	sudo -u "$runas" scp -q -P "$PORT" "$tmp/$name" "$tmp/$name.sha256" "$TO:" ||
		die "scp thất bại" "Server cũ đang dừng: sửa lỗi rồi chạy lại export, hoặc sudo systemctl enable --now scs-server"
	sudo -u "$runas" ssh -p "$PORT" "$TO" "chmod 600 '$name' '$name.sha256' && sha256sum -c '$name.sha256'" >/dev/null ||
		die "SHA-256 ở máy mới KHÔNG khớp" "Chạy lại export; không import gói lỗi"
	ok "Đã chép, SHA-256 ở máy mới khớp"
	wipe "$tmp"
	trap - EXIT
	ok "Đã xóa bản tạm ở máy này"
	echo
	echo "  Trên máy mới chạy:"
	echo "    cd <thư mục SCS-System>/server && sudo bash scripts/move-server.sh import --bundle ~/$name --wan-ip <IP WAN mới> [--lan-ip <IP LAN mới>]"
	echo "  Máy cũ giữ nguyên dữ liệu (đang dừng). Khi máy mới chạy ổn 1–2 ngày: xóa sạch máy cũ (xem docs/thiet-ke-cum-server.md mục 17.1)."
}

# ======================================================================= fetch (máy mới kéo gói về)
do_fetch() {
	[[ -n "$FROM" ]] || die "Thiếu --from <user>@<máy cũ>"
	[[ "$FROM" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9.:_-]+$ ]] || die "--from phải có dạng user@máy (IP hoặc tên), không có đường dẫn"
	[[ "$PORT" =~ ^[0-9]{1,5}$ ]] || die "--port không hợp lệ"
	local runas="${SUDO_USER:-}"
	[[ -n "$runas" && "$runas" != root ]] ||
		die "Chạy bằng sudo từ user thường (khóa SSH tới máy cũ là của user đó)" "Đăng nhập user thường rồi: sudo bash $0 fetch --from ..."
	local home
	home="$(getent passwd "$runas" | cut -d: -f6)"
	step "1. Tìm gói trên máy cũ"
	local name
	name="$(sudo -u "$runas" ssh -p "$PORT" -o BatchMode=yes -o ConnectTimeout=10 "$FROM" 'ls -t scs-move-*.tar.gz 2>/dev/null | head -n1')" ||
		die "Không SSH được tới $FROM bằng khóa của $runas" "Kiểm tra: ssh -p $PORT $FROM (dùng khóa SSH); chấp nhận khóa máy cũ lần đầu bằng tay"
	[[ "$name" =~ ^scs-move-[0-9]{8}-[0-9]{6}\.tar\.gz$ ]] || die "Máy cũ chưa có gói" "Trên máy cũ chạy: sudo bash scripts/move-server.sh export --pull"
	ok "Gói: $name"
	step "2. Kéo về qua SSH, kiểm SHA-256"
	sudo -u "$runas" scp -q -P "$PORT" "$FROM:$name" "$FROM:$name.sha256" "$home/" || die "scp thất bại"
	chmod 600 "$home/$name" "$home/$name.sha256"
	(cd "$home" && sha256sum -c "$name.sha256" >/dev/null) || {
		wipe "$home/$name" "$home/$name.sha256"
		die "SHA-256 KHÔNG khớp (đã xóa bản kéo về)" "Chạy lại fetch"
	}
	ok "SHA-256 khớp: $home/$name"
	if sudo -u "$runas" ssh -p "$PORT" "$FROM" "shred -u -z '$name' '$name.sha256' 2>/dev/null || rm -f '$name' '$name.sha256'"; then
		ok "Đã xóa gói trên máy cũ"
	else
		warn "Chưa xóa được gói trên máy cũ: trên máy cũ chạy sudo bash scripts/move-server.sh clean"
	fi
	echo
	echo "  Tiếp: sudo bash scripts/move-server.sh import --bundle $home/$name --wan-ip <IP WAN máy này> [--lan-ip <IP LAN>]"
}

# ======================================================================= clean (máy cũ xóa gói còn sót)
do_clean() {
	local runas="${SUDO_USER:-root}" home n=0 f
	home="$(getent passwd "$runas" | cut -d: -f6)"
	for f in "$home"/scs-move-*.tar.gz "$home"/scs-move-*.tar.gz.sha256 /root/scs-move.*; do
		[[ -e "$f" ]] || continue
		wipe "$f"
		n=$((n + 1))
	done
	ok "Đã xóa $n file / thư mục gói chuyển server"
}

# ======================================================================= import (máy mới)
do_import() {
	[[ -n "$BUNDLE" && -f "$BUNDLE" ]] || die "Thiếu --bundle <file .tar.gz> (hoặc file không có)"
	[[ -f "$BUNDLE.sha256" ]] || die "Thiếu $BUNDLE.sha256 đi kèm"
	command -v docker >/dev/null || die "Chưa có Docker" "Chạy menu mục 2 (chuẩn bị máy) trước"
	[[ -f "$SERVER_DIR/.env" ]] || die "Chưa có server/.env" "Chạy menu mục 2 (chuẩn bị máy) trước"

	step "1. Kiểm tra gói"
	(cd "$(dirname "$BUNDLE")" && sha256sum -c "$(basename "$BUNDLE").sha256" >/dev/null) || die "SHA-256 của gói KHÔNG khớp" "Chép lại từ máy cũ; không dùng gói lỗi"
	ok "SHA-256 khớp"
	tmp="$(mktemp -d /root/scs-move.XXXXXX)"
	chmod 700 "$tmp"
	trap '[[ -n "$tmp" ]] && wipe "$tmp"' EXIT
	tar -C "$tmp" -xzf "$BUNDLE" MANIFEST db/scs.sql.gz files.tar
	grep -qx "manifest=$MANIFEST_VER" "$tmp/MANIFEST" || die "Gói không đúng định dạng của script này"
	local version commit db_user db_name
	version="$(sed -n 's/^version=//p' "$tmp/MANIFEST")"
	commit="$(sed -n 's/^commit=//p' "$tmp/MANIFEST")"
	echo "  Gói tạo lúc $(sed -n 's/^created=//p' "$tmp/MANIFEST") từ server bản $version"
	if [[ -n "$commit" ]] && git -C "$REPO_DIR" rev-parse --verify --quiet "$commit^{commit}" >/dev/null; then
		git -C "$REPO_DIR" merge-base --is-ancestor "$commit" HEAD ||
			die "Code ở máy này CŨ hơn server cũ ($version)" "cd $REPO_DIR && git pull (hoặc checkout đúng bản), rồi chạy lại import"
		ok "Code máy này cùng hoặc mới hơn bản server cũ"
	else
		warn "Không đối chiếu được bản code ($version) — máy này cần code cùng hoặc mới hơn máy cũ (DB có thể đã đổi cấu trúc)"
		confirm "Vẫn tiếp tục?" || exit 0
	fi

	if [[ -e "$ETC_DIR" || -e "$DATA_DIR" ]]; then
		[[ $FORCE -eq 1 ]] || die "Máy này đã có SCS-Server ($ETC_DIR / $DATA_DIR)" \
			"Không ghi đè server đang có. Chắc chắn muốn thay: thêm --force (cất bản cũ sang *.before-move-<giờ>)"
	fi
	[[ ${#INSTALL_ARGS[@]} -gt 0 ]] ||
		warn "Không có --wan-ip / --lan-ip: chứng chỉ server giữ địa chỉ cũ — agent gọi bằng địa chỉ mới sẽ lỗi TLS. Nên thêm địa chỉ của máy này"
	confirm "Cài server từ gói này lên máy này (khôi phục DB, CA, khóa, cấu hình)?" || exit 0

	step "2. Đặt cấu hình, khóa, dữ liệu"
	systemctl stop scs-server 2>/dev/null || true
	local stamp
	stamp="$(date +%Y%m%d-%H%M%S)"
	for p in "$ETC_DIR" "$DATA_DIR"; do
		if [[ -e "$p" ]]; then
			mv "$p" "$p.before-move-$stamp"
			warn "Đã cất $p cũ sang $p.before-move-$stamp"
		fi
	done
	if ! id "$SVC_USER" >/dev/null 2>&1; then
		useradd --system --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
	fi
	tar -C / -xpf "$tmp/files.tar"
	# Chủ sở hữu theo đúng quy ước của install.sh (uid của scs ở máy mới có thể khác máy cũ).
	chown root:root "$ETC_DIR"; chmod 755 "$ETC_DIR"
	chown "$SVC_USER:$SVC_USER" "$ETC_DIR/server.env"; chmod 600 "$ETC_DIR/server.env"
	if [[ -d "$ETC_DIR/pki" ]]; then
		chown -R "$SVC_USER:$SVC_USER" "$ETC_DIR/pki"
		chmod 700 "$ETC_DIR/pki"
	fi
	chown -R "$SVC_USER:$SVC_USER" "$DATA_DIR"; chmod 700 "$DATA_DIR"
	ok "Đã đặt $ETC_DIR (khóa CA, khóa phát hành, server.env) và $DATA_DIR"

	step "3. Khôi phục DB"
	db_user="$(env_value "$SERVER_DIR/.env" SCS_DB_USER)"; db_user="${db_user:-scs}"
	db_name="$(env_value "$SERVER_DIR/.env" SCS_DB_NAME)"; db_name="${db_name:-scs}"
	(cd "$SERVER_DIR" && dc up -d postgres >/dev/null 2>&1) || true
	for _ in $(seq 1 30); do
		dc exec -T postgres pg_isready -U "$db_user" >/dev/null 2>&1 && break
		sleep 2
	done
	dc exec -T postgres pg_isready -U "$db_user" >/dev/null 2>&1 || die "PostgreSQL chưa chạy" "cd $SERVER_DIR && docker compose -f compose.dev.yml up -d postgres"
	if dc exec -T postgres psql -U "$db_user" -d "$db_name" -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public'" 2>/dev/null | grep -qv '^0$'; then
		mkdir -p "$SERVER_DIR/data/backup"; chmod 700 "$SERVER_DIR/data/backup"
		dc exec -T postgres pg_dump -U "$db_user" -d "$db_name" | gzip >"$SERVER_DIR/data/backup/scs-$stamp-truoc-chuyen-server.sql.gz"
		chmod 600 "$SERVER_DIR/data/backup/scs-$stamp-truoc-chuyen-server.sql.gz"
		warn "DB máy này đã có bảng: đã sao lưu sang data/backup/scs-$stamp-truoc-chuyen-server.sql.gz"
	fi
	dc exec -T postgres dropdb -U "$db_user" --if-exists "$db_name"
	dc exec -T postgres createdb -U "$db_user" "$db_name"
	dc exec -T postgres psql -U "$db_user" -d "$db_name" -q -c "CREATE EXTENSION IF NOT EXISTS timescaledb; SELECT timescaledb_pre_restore();" >/dev/null
	gzip -dc "$tmp/db/scs.sql.gz" | dc exec -T postgres psql -U "$db_user" -d "$db_name" -q -v ON_ERROR_STOP=0 >/tmp/scs-move-restore.log 2>&1 || true
	dc exec -T postgres psql -U "$db_user" -d "$db_name" -q -c "SELECT timescaledb_post_restore();" >/dev/null
	local n errs
	errs="$(grep -c 'ERROR' /tmp/scs-move-restore.log || true)"
	if [[ "${errs:-0}" -gt 0 ]]; then
		warn "Khôi phục DB có $errs dòng ERROR (một số là bình thường với TimescaleDB, ví dụ extension đã có):"
		grep -m 8 'ERROR' /tmp/scs-move-restore.log | sed 's/^/      /'
		confirm "Kiểm tra các lỗi trên — vẫn tiếp tục cài?" || die "Dừng theo yêu cầu" "Dữ liệu đã đặt; DB đang dở — chạy lại import với --force sau khi xử lý"
	fi
	n="$(dc exec -T postgres psql -U "$db_user" -d "$db_name" -tAc "SELECT count(*) FROM devices" 2>/dev/null | tr -d '[:space:]')"
	[[ "$n" =~ ^[0-9]+$ ]] || die "Khôi phục DB lỗi (không đọc được bảng devices)" "Xem /tmp/scs-move-restore.log"
	ok "Đã khôi phục DB: $n máy (log: /tmp/scs-move-restore.log)"

	step "4. Cài dịch vụ (giữ CA cũ, thêm địa chỉ máy này vào chứng chỉ server)"
	# install.sh tự chép mật khẩu DB / NATS của máy này (server/.env) sang server.env, build, cài, khởi động.
	bash "$SERVER_DIR/scripts/install.sh" "${INSTALL_ARGS[@]}" || die "install.sh lỗi" "Xem thông báo phía trên; dữ liệu đã đặt xong, sửa lỗi rồi chạy lại: sudo bash scripts/install.sh ${INSTALL_ARGS[*]}"
	wipe "$tmp"
	trap - EXIT
	echo
	ok "Xong. Kiểm tra: menu mục 9 (trạng thái), đăng nhập web, trang Máy thấy máy gọi về (5–30 phút)."
	if confirm "Xóa gói $BUNDLE ở máy này (chứa khóa và dữ liệu)?"; then
		wipe "$BUNDLE" "$BUNDLE.sha256"
		ok "Đã xóa gói"
	else
		warn "Nhớ xóa gói khi không cần: shred -u $BUNDLE"
	fi
}

case "$MODE" in
	export) do_export ;;
	import) do_import ;;
	fetch) do_fetch ;;
	clean) do_clean ;;
esac
