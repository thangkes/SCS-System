#!/usr/bin/env bash
# update.sh - cập nhật SCS-Server đang chạy (dịch vụ systemd cài bằng scripts/install.sh).
#
# Cách dùng (chạy bằng user thường sở hữu thư mục code, script tự gọi sudo khi cài):
#   bash scripts/update.sh                 cập nhật lên tag phát hành mới nhất (v*); chưa có tag thì lên origin/main
#   bash scripts/update.sh --ref v0.2.0    cập nhật lên đúng tag / nhánh / commit chỉ định
#   bash scripts/update.sh --check         chỉ xem có bản mới không, không làm gì
#   bash scripts/update.sh --rollback      quay về binary trước lần cập nhật gần nhất
#
# Tùy chọn: --skip-tests (không chạy go test), --yes (không hỏi xác nhận)
#
# Các bước: kéo code (git fetch) → so bản đang chạy → chạy test → build → nếu bản mới có thay đổi
# cấu trúc DB thì sao lưu DB → cài (giữ binary cũ thành scs-server.prev) → kiểm /readyz;
# không lên thì tự quay về binary cũ. Thư mục code có thay đổi chưa commit thì không chạy.

# Chạy bằng sh (dash) thì tự chuyển sang bash: script dùng cú pháp của bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Eeuo pipefail

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$SERVER_DIR/.." && pwd)"
OPT_DIR=/opt/scs-server
ETC_DIR=/etc/scs-server
ENV_FILE=$ETC_DIR/server.env
COMPOSE_FILE="$SERVER_DIR/compose.dev.yml"
BACKUP_DIR="$SERVER_DIR/data/backup"

REF="" MODE=update SKIP_TESTS=0 YES=0

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
trap 'die "Lệnh ở dòng $LINENO thất bại" "Xem thông báo phía trên"' ERR

while [[ $# -gt 0 ]]; do
	case "$1" in
		--ref) REF="${2:-}"; shift 2 ;;
		--check) MODE=check; shift ;;
		--rollback) MODE=rollback; shift ;;
		--skip-tests) SKIP_TESTS=1; shift ;;
		--yes | -y) YES=1; shift ;;
		-h | --help) sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Tham số không hợp lệ: $1" "Xem: bash $0 --help" ;;
	esac
done

[[ $EUID -ne 0 ]] || die "Không chạy bằng root" "Chạy bằng user sở hữu thư mục code; script tự gọi sudo ở bước cài"
command -v systemctl >/dev/null || die "Máy không dùng systemd"
[[ -x "$OPT_DIR/scs-server" ]] || die "Chưa cài dịch vụ scs-server" "Cài lần đầu: sudo bash scripts/install.sh --wan-ip <IP WAN>"

confirm() {
	[[ $YES -eq 1 ]] && return 0
	local a
	read -r -p "  $1 [y/N] " a
	[[ "$a" =~ ^[yY]$ ]]
}

# env_value đọc một biến trong file .env (không source file).
env_value() {
	local line
	line="$(sudo grep -E "^(export[[:space:]]+)?$1=" "$ENV_FILE" 2>/dev/null | tail -n1 || true)"
	line="${line#*=}"
	line="$(sed -E 's/^[[:space:]]*//; s/^#.*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//' <<<"$line")"
	line="${line#\"}"; line="${line%\"}"
	printf '%s' "$line"
}

# ready: chờ tối đa 60 giây cho tới khi cổng agent trả /readyz "ok" (DB kết nối được, cấu trúc DB đã cập nhật).
ready() {
	local port listen out
	listen="$(env_value SCS_INGEST_LISTEN)"
	port="${listen##*:}"
	port="${port:-6222}"
	for _ in $(seq 1 30); do
		if systemctl is-active --quiet scs-server; then
			if [[ -r "$ETC_DIR/agent-ca.pem" ]]; then
				out="$(curl -sS --max-time 3 --cacert "$ETC_DIR/agent-ca.pem" "https://127.0.0.1:$port/readyz" 2>/dev/null || true)"
			else
				out="$(curl -sS --max-time 3 "http://127.0.0.1:$port/readyz" 2>/dev/null || true)"
			fi
			[[ "$out" == *'"status":"ok"'* ]] && return 0
		fi
		sleep 2
	done
	echo "  /readyz cuối cùng: ${out:-không trả lời}"
	return 1
}

rollback() {
	[[ -x "$OPT_DIR/scs-server.prev" ]] || die "Không có binary cũ ($OPT_DIR/scs-server.prev) để quay về"
	sudo cp -p "$OPT_DIR/scs-server" "$OPT_DIR/scs-server.failed"
	sudo cp -p "$OPT_DIR/scs-server.prev" "$OPT_DIR/scs-server"
	sudo systemctl restart scs-server
	if ready; then
		ok "Đã quay về bản trước: $("$OPT_DIR/scs-server" version) (bản lỗi giữ ở $OPT_DIR/scs-server.failed)"
	else
		sudo journalctl -u scs-server -n 30 --no-pager >&2 || true
		die "Quay về bản trước nhưng server vẫn chưa sẵn sàng" \
			"Nếu bản mới đã đổi cấu trúc DB, bản cũ không chạy được với DB mới: khôi phục bản sao lưu trong $BACKUP_DIR hoặc sửa lỗi bản mới"
	fi
}

if [[ $MODE == rollback ]]; then
	step "Quay về bản trước"
	echo "  Đang chạy: $("$OPT_DIR/scs-server" version)"
	echo "  Bản trước: $("$OPT_DIR/scs-server.prev" version 2>/dev/null || echo "không có")"
	confirm "Quay về bản trước?" || exit 0
	rollback
	exit 0
fi

# ---------------------------------------------------------------- 1. xem bản mới
step "1. Kiểm tra bản mới"
cd "$REPO_DIR"
git rev-parse --git-dir >/dev/null 2>&1 || die "$REPO_DIR không phải thư mục git" "Clone lại repo bằng git để cập nhật được"
[[ -z "$(git status --porcelain --untracked-files=no)" ]] ||
	die "Thư mục code có thay đổi chưa commit" "Xem: git status; bỏ thay đổi (git checkout -- .) hoặc commit trước khi cập nhật"
git fetch --quiet --tags origin || die "Không kéo được code từ GitHub" "Kiểm tra mạng và quyền truy cập repo (gh auth status hoặc deploy key)"

if [[ -z "$REF" ]]; then
	REF="$(git tag -l 'v*' --sort=-v:refname | head -n1)"
	[[ -n "$REF" ]] || REF=origin/main
fi
target="$(git rev-parse --verify --quiet "$REF^{commit}")" || die "Không tìm thấy \"$REF\" trong repo"
current="$(git rev-parse HEAD)"
running="$("$OPT_DIR/scs-server" version 2>/dev/null || echo "?")"
echo "  Đang chạy : $running (code ở $(git describe --tags --always "$current"))"
echo "  Bản mới   : $REF ($(git describe --tags --always "$target"))"

if [[ "$target" == "$current" && "$running" == "$(git describe --tags --always --dirty)" ]]; then
	ok "Đã là bản mới nhất"
	exit 0
fi
if [[ "$target" != "$current" ]]; then
	echo "  Thay đổi:"
	git log --oneline --no-decorate "$current..$target" | head -n 20 | sed 's/^/    /'
	if git merge-base --is-ancestor "$target" "$current"; then
		warn "\"$REF\" CŨ HƠN bản code hiện tại (hạ phiên bản)"
	fi
fi
migrations_changed=0
if [[ -n "$(git diff --name-only "$current" "$target" -- server/internal/store/migrations)" ]]; then
	migrations_changed=1
	warn "Bản mới có thay đổi cấu trúc DB: sẽ sao lưu DB trước khi cài (sau khi đổi, bản cũ có thể không chạy được với DB mới)"
fi
[[ $MODE == check ]] && exit 0
confirm "Cập nhật lên $REF?" || exit 0

# ---------------------------------------------------------------- 2. build + test
step "2. Build và test"
GO="$(command -v go || echo /usr/local/go/bin/go)"
[[ -x "$GO" ]] || die "Không có go" "Chạy scripts/setup.sh để cài Go"
git checkout --quiet --detach "$target"
restore() { git checkout --quiet "$current"; }
version="$(git describe --tags --always)"
if [[ $SKIP_TESTS -eq 0 ]]; then
	if ! (cd "$SERVER_DIR" && "$GO" test ../common/... ./... >/tmp/scs-update-test.log 2>&1); then
		tail -n 30 /tmp/scs-update-test.log >&2
		restore
		die "Test bản mới thất bại, KHÔNG cài; code đã trả về bản cũ" "Log đầy đủ: /tmp/scs-update-test.log"
	fi
	ok "Test qua"
fi
if ! (cd "$SERVER_DIR" && CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w -X main.version=$version" -o bin/scs-server ./cmd/scs-server); then
	restore
	die "Build thất bại, KHÔNG cài; code đã trả về bản cũ"
fi
ok "Đã build scs-server $version"

# ---------------------------------------------------------------- 3. sao lưu DB (khi đổi cấu trúc)
if [[ $migrations_changed -eq 1 ]]; then
	step "3. Sao lưu DB"
	mkdir -p "$BACKUP_DIR"
	chmod 700 "$BACKUP_DIR"
	file="$BACKUP_DIR/scs-$(date +%Y%m%d-%H%M%S)-truoc-$version.sql.gz"
	dbuser="$(env_value SCS_DB_USER)"
	dbname="$(env_value SCS_DB_NAME)"
	if docker compose -f "$COMPOSE_FILE" ps --status running -q postgres 2>/dev/null | grep -q . ||
		sudo docker compose -f "$COMPOSE_FILE" ps --status running -q postgres 2>/dev/null | grep -q .; then
		dc=(docker compose -f "$COMPOSE_FILE")
		docker info >/dev/null 2>&1 || dc=(sudo docker compose -f "$COMPOSE_FILE")
		if "${dc[@]}" exec -T postgres pg_dump -U "${dbuser:-scs}" -d "${dbname:-scs}" | gzip >"$file" && [[ -s "$file" ]]; then
			chmod 600 "$file"
			ok "Đã sao lưu: $file"
		else
			rm -f "$file"
			restore
			die "Sao lưu DB thất bại, KHÔNG cài" "Kiểm tra container postgres: docker compose -f compose.dev.yml ps"
		fi
	else
		warn "Không thấy container postgres của compose.dev.yml: không tự sao lưu được (DB đặt ở nơi khác?)"
		confirm "Vẫn cài khi CHƯA sao lưu DB?" || { restore; exit 1; }
	fi
fi

# ---------------------------------------------------------------- 4. cài + kiểm
step "4. Cài và kiểm tra"
# Log do user thường ghi (cố ý: không cần sudo cho file log).
# shellcheck disable=SC2024
sudo bash "$SERVER_DIR/scripts/install.sh" >/tmp/scs-update-install.log 2>&1 || {
	tail -n 30 /tmp/scs-update-install.log >&2
	warn "Cài bản mới lỗi, quay về bản trước"
	rollback
	restore
	exit 1
}
if ready; then
	ok "SCS-Server $version đang chạy, /readyz ok"
	echo "  Bản trước giữ ở $OPT_DIR/scs-server.prev; quay về: bash scripts/update.sh --rollback"
else
	sudo journalctl -u scs-server -n 30 --no-pager >&2 || true
	warn "Bản mới không sẵn sàng sau 60 giây, quay về bản trước"
	rollback
	restore
	exit 1
fi
