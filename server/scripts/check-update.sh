#!/usr/bin/env bash
# Kiểm tra có bản SCS-Server mới trên GitHub không (CHỈ KIỂM, không cài). Timer scs-server-update-check chạy
# 6 giờ một lần; web quản trị (mức 0 và 1) đọc kết quả để hiện thông báo kèm danh sách thay đổi.
#
#   check-update.sh --server-dir <thư mục server trong repo> [--out <file>]
#
# Chạy bằng root nhưng mọi lệnh git chạy bằng user sở hữu repo (không chạy git của repo người khác bằng root:
# cấu hình / hook trong repo có thể thực thi lệnh). Kết quả ghi vào thư mục root:scs 750 — dịch vụ scs đọc
# được nhưng không sửa / thay bằng symlink được.
#
# Bản mới chọn giống update.sh: tag phát hành v* mới nhất, chưa có tag thì origin/main. Danh sách thay đổi là
# các commit từ bản ĐANG CHẠY (scs-server version) tới bản mới.
set -uo pipefail
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

SERVER_DIR=""
OUT=/var/lib/scs-update/update-check.txt
OPT_DIR=/opt/scs-server
SVC_GROUP=scs
MAX_COMMITS=60
while [[ $# -gt 0 ]]; do
	case "$1" in
		--server-dir) SERVER_DIR="${2:-}"; shift 2 ;;
		--out) OUT="${2:-}"; shift 2 ;;
		--opt-dir) OPT_DIR="${2:-}"; shift 2 ;; # chỗ cài scs-server (thử nghiệm)
		-h | --help) sed -n '2,13p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "Tham số không hợp lệ: $1" >&2; exit 2 ;;
	esac
done
[[ -n "$SERVER_DIR" && -d "$SERVER_DIR" ]] || { echo "Thiếu --server-dir" >&2; exit 2; }
REPO_DIR="$(cd "$SERVER_DIR/.." && pwd)"
OUT_DIR="$(dirname "$OUT")"

# g: git chạy bằng chủ repo (root chỉ khi repo thuộc root).
owner="$(stat -c %U "$REPO_DIR")"
g() {
	if [[ $EUID -eq 0 && "$owner" != root ]]; then
		runuser -u "$owner" -- git -C "$REPO_DIR" "$@"
	else
		git -C "$REPO_DIR" "$@"
	fi
}

# Thư mục kết quả root:scs 750 (scs đọc được, không ghi được); file tạm tạo ngay trong đó để đổi tên nguyên khối.
install -d -o root -g "$SVC_GROUP" -m 750 "$OUT_DIR" 2>/dev/null || install -d -m 755 "$OUT_DIR"
tmp="$(mktemp "$OUT_DIR/.update-check.XXXXXX")" || { echo "Không tạo được file tạm trong $OUT_DIR" >&2; exit 1; }
trap 'rm -f "$tmp"' EXIT
err=""
running="$("$OPT_DIR/scs-server" version 2>/dev/null | head -n1 | tr -cd '[:alnum:]._+-')"
[[ -n "$running" ]] || running="?"

if [[ $EUID -eq 0 && "$owner" != root ]]; then
	fetch=(timeout 120 runuser -u "$owner" -- git -C "$REPO_DIR" fetch --quiet --tags origin)
else
	fetch=(timeout 120 git -C "$REPO_DIR" fetch --quiet --tags origin)
fi
if ! "${fetch[@]}" 2>/dev/null; then
	err="không kéo được code từ GitHub (mạng / quyền truy cập repo)"
fi
ref="$(g tag -l 'v*' --sort=-v:refname 2>/dev/null | head -n1)"
[[ -n "$ref" ]] || ref=origin/main
target="$(g rev-parse --verify --quiet "$ref^{commit}" 2>/dev/null || true)"

# Commit của bản đang chạy: version = git describe (vX-N-gHASH, tag, hoặc HASH; có thể kèm -dirty).
v="${running%-dirty}"
[[ "$v" == *-g* ]] && v="${v##*-g}"
base="$(g rev-parse --verify --quiet "$v^{commit}" 2>/dev/null || true)"
if [[ -z "$base" ]]; then
	base="$(g rev-parse --verify --quiet HEAD 2>/dev/null || true)"
	[[ -n "$err" ]] || err="không xác định được commit của bản đang chạy ($running), so với code trên máy"
fi

behind=0
migrations=0
if [[ -n "$target" && -n "$base" ]]; then
	behind="$(g rev-list --count "$base..$target" 2>/dev/null || echo 0)"
	[[ "$behind" -gt 0 && -n "$(g log --format=%h "$base..$target" -- server/internal/store/migrations 2>/dev/null)" ]] && migrations=1
elif [[ -z "$err" ]]; then
	err="không tìm thấy $ref trong repo"
fi

{
	echo "SCS-UPDATE-CHECK 1"
	echo "checked=$(date -Iseconds)"
	echo "running=$running"
	echo "target=$ref"
	echo "target_desc=$(g describe --tags --always "$target" 2>/dev/null | tr -cd '[:alnum:]._+/-')"
	echo "behind=$behind"
	echo "migrations=$migrations"
	echo "error=$(tr -d '\r\n' <<<"$err")"
	echo "--"
	if [[ "$behind" -gt 0 ]]; then
		# Mỗi commit: \x1e mã \x1f ngày \x1f tiêu đề \x1f nội dung (server tự cắt độ dài).
		g log --no-decorate --no-merges -n "$MAX_COMMITS" --format='%x1e%h%x1f%cI%x1f%s%x1f%b' "$base..$target" 2>/dev/null
	fi
} >"$tmp"

chmod 640 "$tmp"
chgrp "$SVC_GROUP" "$tmp" 2>/dev/null || chmod 644 "$tmp"
mv -f "$tmp" "$OUT"
trap - EXIT
echo "Đang chạy $running; bản mới nhất $ref: $behind thay đổi${err:+ (lỗi: $err)}"
