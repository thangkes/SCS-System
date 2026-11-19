#!/usr/bin/env bash
# release-agent.sh - build bản agent mới và nạp vào kho phát hành của server ĐANG CHẠY, không cài lại
# và không khởi động lại scs-server (menu mục 19).
#
# Cách dùng (user thường sở hữu thư mục code; script tự gọi sudo khi chép):
#   bash scripts/release-agent.sh                 build agent từ tag phát hành mới nhất (v*); chưa có tag thì origin/main
#   bash scripts/release-agent.sh --ref <tag|commit>
#   Tùy chọn: --skip-tests, --yes
#
# Các bước: git fetch → build trong thư mục tạm (git worktree), KHÔNG đổi code của server → chạy test
# của agent + common → build amd64/arm64 → chép vào /opt/scs-server/agent (binary trước, VERSION sau
# cùng) và server/bin/agent (cho menu mục 11 đóng gói). scs-server tự nạp trong khoảng 1 phút, ký bằng
# khóa phát hành; sau đó phát hành trên trang Agent như thường.
#
# Chỉ dùng cho thay đổi phía agent. Bản có đổi phần server, giao thức (common/protocol) hay cấu trúc DB
# thì cập nhật server (menu mục 4) — script cảnh báo và hỏi lại khi thấy các thay đổi đó.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Eeuo pipefail

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$SERVER_DIR/.." && pwd)"
OPT_DIR=/opt/scs-server
DIST="$OPT_DIR/agent"

REF="" SKIP_TESTS=0 YES=0

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

ARGS=("$@")
while [[ $# -gt 0 ]]; do
	case "$1" in
		--ref) REF="${2:-}"; shift 2 ;;
		--skip-tests) SKIP_TESTS=1; shift ;;
		--yes | -y) YES=1; shift ;;
		-h | --help) sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Tham số không hợp lệ: $1" "Xem: bash $0 --help" ;;
	esac
done

# Chạy bằng root: chuyển sang user sở hữu thư mục code (như update.sh).
if [[ $EUID -eq 0 ]]; then
	owner="$(stat -c %U "$REPO_DIR")"
	if [[ "$owner" != root ]]; then
		echo "  Đang chạy bằng root, chuyển sang user sở hữu thư mục code: $owner"
		exec sudo -u "$owner" -H bash "${BASH_SOURCE[0]}" "${ARGS[@]}"
	fi
fi
[[ -x "$OPT_DIR/scs-server" ]] || die "Chưa cài dịch vụ scs-server" "Cài lần đầu: menu mục 3"

confirm() {
	[[ $YES -eq 1 ]] && return 0
	local a
	read -r -p "  $1 [y/N] " a
	[[ "$a" =~ ^[yY]$ ]]
}

# ---------------------------------------------------------------- 1. chọn bản
step "1. Chọn bản agent"
cd "$REPO_DIR"
git rev-parse --git-dir >/dev/null 2>&1 || die "$REPO_DIR không phải thư mục git"
[[ -z "$(git status --porcelain --untracked-files=no)" ]] ||
	die "Thư mục code có thay đổi chưa commit" "Xem: git status; bỏ thay đổi hoặc commit trước"
git fetch --quiet --tags origin || die "Không kéo được code từ GitHub" "Kiểm tra mạng và quyền truy cập repo"
if [[ -z "$REF" ]]; then
	REF="$(git tag -l 'v*' --sort=-v:refname | head -n1)"
	[[ -n "$REF" ]] || REF=origin/main
fi
target="$(git rev-parse --verify --quiet "$REF^{commit}")" || die "Không tìm thấy \"$REF\" trong repo"
version="$(git describe --tags --always "$target")"
running="$("$OPT_DIR/scs-server" version 2>/dev/null || echo "?")"
have="$(cat "$DIST/VERSION" 2>/dev/null || echo "chưa có")"
echo "  Server đang chạy : $running"
echo "  Bản agent đang có: $have"
echo "  Bản agent sẽ build: $version ($REF)"
[[ "$have" != "$version" ]] || { ok "Bản agent $version đã có trong kho, không cần build"; exit 0; }

# Bản mới so với code của server đang chạy: đổi phần server / giao thức / DB thì nên cập nhật server.
base="$(git rev-parse --verify --quiet "${running%-dirty}^{commit}" || true)"
if [[ -z "$base" ]]; then
	warn "Không xác định được commit của server đang chạy ($running): không kiểm được tương thích"
	confirm "Vẫn build bản agent?" || exit 0
else
	echo "  Thay đổi phía agent từ bản server đang chạy:"
	git log --oneline --no-decorate "$base..$target" -- agent common | head -n 15 | sed 's/^/    /'
	risky="$(git diff --name-only "$base" "$target" -- server common/protocol | head -n 10)"
	if [[ -n "$risky" ]]; then
		warn "Bản này có đổi phần server / giao thức — agent mới có thể cần server mới. Nên cập nhật server (menu mục 4):"
		sed 's/^/      /' <<<"$risky"
		confirm "Vẫn chỉ phát hành bản agent (server giữ nguyên)?" || exit 0
	else
		confirm "Build và nạp bản agent $version?" || exit 0
	fi
fi

# ---------------------------------------------------------------- 2. build trong thư mục tạm
step "2. Test và build (thư mục tạm, code của server không đổi)"
GO="$(command -v go || echo /usr/local/go/bin/go)"
[[ -x "$GO" ]] || die "Không có go" "Chạy scripts/setup.sh để cài Go"
wt="$(mktemp -d /tmp/scs-agent-build.XXXXXX)"
cleanup() { git -C "$REPO_DIR" worktree remove --force "$wt" >/dev/null 2>&1 || rm -rf "$wt"; }
trap cleanup EXIT
git worktree add --quiet --detach "$wt" "$target"
if [[ $SKIP_TESTS -eq 0 ]]; then
	if ! (cd "$wt/agent" && "$GO" test ./... && cd "$wt/common" && "$GO" test ./...) >/tmp/scs-agent-test.log 2>&1; then
		tail -n 30 /tmp/scs-agent-test.log >&2
		die "Test agent thất bại, KHÔNG nạp" "Log đầy đủ: /tmp/scs-agent-test.log"
	fi
	ok "Test agent + common qua"
fi
bash "$wt/server/scripts/build-agent.sh" "$version" || die "Build agent thất bại, KHÔNG nạp"
out="$wt/server/bin/agent"

# ---------------------------------------------------------------- 3. nạp
step "3. Nạp vào kho phát hành"
# Binary trước (chép ra file tạm rồi đổi tên), VERSION sau cùng: server chỉ nhập khi VERSION đổi, nên
# không bao giờ đọc phải binary đang chép dở.
sudo install -d -o root -g root -m 755 "$DIST"
for f in "$out"/scs-agent-linux-*; do
	n="$(basename "$f")"
	sudo install -o root -g root -m 644 "$f" "$DIST/.$n.new"
	sudo mv -f "$DIST/.$n.new" "$DIST/$n"
done
sudo install -o root -g root -m 644 "$out/VERSION" "$DIST/.VERSION.new"
sudo mv -f "$DIST/.VERSION.new" "$DIST/VERSION"
# Bản trong thư mục code (menu mục 11 đóng gói từ đây).
mkdir -p "$SERVER_DIR/bin/agent"
rm -f "$SERVER_DIR/bin/agent"/scs-agent-linux-* "$SERVER_DIR/bin/agent/VERSION"
cp -p "$out"/scs-agent-linux-* "$out/VERSION" "$SERVER_DIR/bin/agent/"
ok "Đã chép bản agent $version vào $DIST"

echo "  Chờ server nạp (tối đa 90 giây)…"
since="$(date '+%Y-%m-%d %H:%M:%S' -d '-5 seconds')"
for _ in $(seq 1 45); do
	if sudo journalctl -u scs-server --since "$since" --no-pager -q 2>/dev/null | grep -q "đã nhập bản agent vào kho phát hành"; then
		ok "Server đã nạp và ký bản agent $version — phát hành trên trang Agent (nhóm thử trước)"
		exit 0
	fi
	sleep 2
done
warn "Chưa thấy server nạp sau 90 giây. Server bản cũ (trước khi có tự nạp) chỉ nạp khi khởi động: sudo systemctl restart scs-server"
