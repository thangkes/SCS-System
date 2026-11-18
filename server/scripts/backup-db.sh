#!/usr/bin/env bash
# backup-db.sh - sao lưu DB của SCS-Server (PostgreSQL + TimescaleDB chạy bằng compose.dev.yml) ra file .sql.gz.
#
#   sudo bash scripts/backup-db.sh                       sao lưu ngay (bản "tay")
#   sudo bash scripts/backup-db.sh --reason dinh-ky      bản định kỳ (timer 15 ngày gọi), xóa bớt bản định kỳ cũ
#   sudo bash scripts/backup-db.sh --list                xem các bản đang có
#
# Tùy chọn: --server-dir DIR (thư mục server/ có compose.dev.yml; mặc định: thư mục chứa script này),
# --dir DIR (nơi lưu; mặc định <server>/data/backup), --keep N (giữ N bản định kỳ mới nhất; mặc định
# SCS_BACKUP_KEEP trong server.env, không có thì 6 ≈ 3 tháng). Chỉ xóa bản định kỳ; bản trước cập nhật
# và bản tay không bao giờ tự xóa.
#
# File: scs-<ngày-giờ>-<lý do>.sql.gz, quyền 600, thư mục 700. Ghi ra file tạm, kiểm gzip đọc được và
# có nội dung rồi mới đổi tên — bản lỗi không bao giờ thay bản tốt. Lỗi thì thoát mã khác 0 (timer báo
# failed trong `systemctl status scs-server-backup`).
#
# Khôi phục (TimescaleDB cần bước pre/post restore) — xem server/README.md, mục "Sao lưu DB".

set -Eeuo pipefail

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REASON=tay
KEEP=""
DIR=""
MODE=backup
while [[ $# -gt 0 ]]; do
	case "$1" in
		--server-dir) SERVER_DIR="$(cd "$2" && pwd)"; shift 2 ;;
		--reason) REASON="$2"; shift 2 ;;
		--keep) KEEP="$2"; shift 2 ;;
		--dir) DIR="$2"; shift 2 ;;
		--list) MODE=list; shift ;;
		-h | --help) sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "Không hiểu tùy chọn: $1 (xem --help)" >&2; exit 2 ;;
	esac
done
[[ "$REASON" =~ ^[a-z0-9][a-z0-9._-]{0,40}$ ]] || { echo "--reason chỉ gồm chữ thường, số, . _ -" >&2; exit 2; }
DIR="${DIR:-$SERVER_DIR/data/backup}"
COMPOSE_FILE="$SERVER_DIR/compose.dev.yml"
ENV_FILE=/etc/scs-server/server.env
[[ -r "$ENV_FILE" ]] || ENV_FILE="$SERVER_DIR/.env"

log() { printf '%s %s\n' "$(date '+%F %T')" "$*"; }
die() { log "LỖI: $*" >&2; exit 1; }

# env_value đọc một biến trong file .env (không source file, không chạy gì trong đó).
env_value() {
	local line
	line="$(grep -E "^(export[[:space:]]+)?$1=" "$ENV_FILE" 2>/dev/null | tail -n1 || true)"
	line="${line#*=}"
	line="$(sed -E 's/^[[:space:]]*//; s/^#.*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//' <<<"$line")"
	line="${line#\"}"; line="${line%\"}"
	printf '%s' "$line"
}

list() {
	if ! ls "$DIR"/scs-*.sql.gz >/dev/null 2>&1; then
		echo "Chưa có bản sao lưu nào trong $DIR"
		return 0
	fi
	echo "Các bản sao lưu trong $DIR (mới nhất trước):"
	# shellcheck disable=SC2012
	ls -1t "$DIR"/scs-*.sql.gz | while read -r f; do
		printf '  %-60s %8s\n' "$(basename "$f")" "$(du -h "$f" | cut -f1)"
	done
}

if [[ $MODE == list ]]; then
	list
	exit 0
fi

[[ -z "$KEEP" ]] && KEEP="$(env_value SCS_BACKUP_KEEP)"
KEEP="${KEEP:-6}"
[[ "$KEEP" =~ ^[0-9]+$ && "$KEEP" -ge 1 && "$KEEP" -le 100 ]] || die "số bản giữ (--keep / SCS_BACKUP_KEEP) phải từ 1 tới 100"
[[ -r "$COMPOSE_FILE" ]] || die "không thấy $COMPOSE_FILE (DB chạy nơi khác thì tự sao lưu bằng pg_dump)"

dc=(docker compose -f "$COMPOSE_FILE")
if ! docker info >/dev/null 2>&1; then
	[[ $EUID -eq 0 ]] && die "docker không chạy"
	dc=(sudo docker compose -f "$COMPOSE_FILE")
fi
"${dc[@]}" ps --status running -q postgres 2>/dev/null | grep -q . || die "container postgres của compose.dev.yml không chạy"

dbuser="$(env_value SCS_DB_USER)"; dbuser="${dbuser:-scs}"
dbname="$(env_value SCS_DB_NAME)"; dbname="${dbname:-scs}"
[[ "$dbuser" =~ ^[A-Za-z0-9_]+$ && "$dbname" =~ ^[A-Za-z0-9_]+$ ]] || die "SCS_DB_USER / SCS_DB_NAME lạ"

umask 077
mkdir -p "$DIR"
chmod 700 "$DIR"

# Còn chỗ trống: ít nhất gấp đôi bản mới nhất (hoặc 200 MB khi chưa có bản nào).
need=$((200 * 1024))
last="$(ls -1t "$DIR"/scs-*.sql.gz 2>/dev/null | head -n1 || true)"
[[ -n "$last" ]] && need=$(( $(du -k "$last" | cut -f1) * 2 + 10240 ))
free="$(df -Pk "$DIR" | awk 'NR==2 {print $4}')"
[[ "$free" -ge "$need" ]] || die "ổ đĩa còn $((free / 1024)) MB, cần ít nhất $((need / 1024)) MB để sao lưu"

file="$DIR/scs-$(date +%Y%m%d-%H%M%S)-$REASON.sql.gz"
tmp="$file.dang-ghi"
trap 'rm -f "$tmp"' EXIT
log "Sao lưu DB $dbname → $file"
"${dc[@]}" exec -T postgres pg_dump -U "$dbuser" -d "$dbname" --no-owner | gzip -6 >"$tmp" || die "pg_dump thất bại"
gzip -t "$tmp" || die "file sao lưu hỏng (gzip -t)"
[[ "$(stat -c %s "$tmp")" -gt 100 ]] || die "file sao lưu rỗng"
# Đọc hết (không cắt ngang pipe, tránh SIGPIPE với pipefail) rồi tìm dòng kết thúc của pg_dump.
ending="$(gzip -dc "$tmp" | tail -n 5)"
[[ "$ending" == *"PostgreSQL database dump complete"* ]] || die "pg_dump chưa ghi xong (thiếu dòng kết thúc)"
mv "$tmp" "$file"
chmod 600 "$file"
trap - EXIT
log "Xong: $(du -h "$file" | cut -f1)"

# Chỉ xóa bản định kỳ cũ, giữ KEEP bản mới nhất.
if [[ "$REASON" == dinh-ky ]]; then
	mapfile -t old < <(ls -1t "$DIR"/scs-*-dinh-ky.sql.gz 2>/dev/null | tail -n +$((KEEP + 1)))
	for f in "${old[@]}"; do
		rm -f -- "$f"
		log "Đã xóa bản định kỳ cũ: $(basename "$f")"
	done
fi
