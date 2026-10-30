#!/usr/bin/env bash
# package-agent.sh - đóng gói SCS-Agent thành MỘT file để cài lên máy tại điểm bán.
#
# Cách dùng (trên máy server, cần Go; menu server mục "Đóng gói agent" gọi script này):
#   bash scripts/package-agent.sh --server https://<IP WAN>:<cổng> [tùy chọn]
#
# Tùy chọn:
#   --server URL    địa chỉ agent gọi tới (IP WAN của server : cổng chuyển tiếp về 6222), đóng sẵn vào gói
#   --ca FILE       CA của server (mặc định /etc/scs-server/agent-ca.pem do install.sh tạo)
#   --watch LIST    dịch vụ systemd mặc định cần báo trạng thái (ví dụ pos.service)
#   --arch ARCH     amd64 (mặc định) hoặc arm64
#   --out DIR       thư mục chứa gói (mặc định server/data/dist)
#
# Gói scs-agent-<phiên bản>-linux-<arch>.tar.gz gồm binary scs-agent build từ code hiện tại, script cài,
# unit systemd, menu, CA của server và địa chỉ server đặt sẵn. Trên máy tại điểm bán chỉ cần:
#   tar xzf scs-agent-*.tar.gz && sudo bash scs-agent/scripts/install.sh
# (hoặc sudo bash scs-agent/menu.sh). Gói không chứa bí mật nào: CA chỉ là chứng chỉ công khai.

# Chạy bằng sh (dash) thì tự chuyển sang bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Eeuo pipefail

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$SERVER_DIR/.." && pwd)"
AGENT_SRC="$REPO_DIR/agent"
SERVER="" CA_FILE=/etc/scs-server/agent-ca.pem WATCH="" ARCH=amd64 OUT_DIR="$SERVER_DIR/data/dist"

if [[ -t 1 ]]; then
	C_RED=$'\e[31m' C_GRN=$'\e[32m' C_B=$'\e[1m' C_0=$'\e[0m'
else
	C_RED='' C_GRN='' C_B='' C_0=''
fi
ok() { printf '  %s✔%s %s\n' "$C_GRN" "$C_0" "$*"; }
die() {
	printf '\n%s✖ LỖI: %s%s\n' "$C_RED$C_B" "$1" "$C_0" >&2
	[[ $# -gt 1 ]] && printf '  Cách xử lý: %s\n' "$2" >&2
	exit 1
}
trap 'die "Lệnh ở dòng $LINENO thất bại" "Xem thông báo phía trên"' ERR

while [[ $# -gt 0 ]]; do
	case "$1" in
		--server) SERVER="${2:-}"; shift 2 ;;
		--ca) CA_FILE="${2:-}"; shift 2 ;;
		--watch) WATCH="${2:-}"; shift 2 ;;
		--arch) ARCH="${2:-}"; shift 2 ;;
		--out) OUT_DIR="${2:-}"; shift 2 ;;
		-h | --help) sed -n '2,19p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Tham số không hợp lệ: $1" "Xem: bash $0 --help" ;;
	esac
done

# ---- kiểm tra đầu vào
[[ -n "$SERVER" ]] || die "Thiếu --server" "Ví dụ: --server https://203.0.113.10:16222 (IP WAN của server : cổng chuyển tiếp về 6222)"
[[ "$SERVER" =~ ^https://[^/:@[:space:]]+:[0-9]{1,5}/?$ ]] || die "--server phải có dạng https://<IP>:<cổng>, đang là \"$SERVER\""
SERVER="${SERVER%/}"
[[ -r "$CA_FILE" ]] || die "Không đọc được CA $CA_FILE" "Cài server trước (menu mục 3) để có /etc/scs-server/agent-ca.pem, hoặc chỉ định --ca"
grep -q "BEGIN CERTIFICATE" "$CA_FILE" || die "$CA_FILE không phải chứng chỉ PEM"
grep -q "PRIVATE KEY" "$CA_FILE" && die "$CA_FILE chứa KHÓA RIÊNG: không được đóng vào gói" "Dùng agent-ca.pem (chỉ chứng chỉ), không dùng ca.key"
case "$ARCH" in amd64 | arm64) ;; *) die "--arch chỉ nhận amd64 hoặc arm64" ;; esac
for s in ${WATCH//,/ }; do
	[[ "$s" =~ ^[A-Za-z0-9@_.:][A-Za-z0-9@_.:-]*$ ]] || die "--watch: \"$s\" không phải tên dịch vụ systemd hợp lệ"
done
GO="$(command -v go || echo /usr/local/go/bin/go)"
[[ -x "$GO" ]] || die "Không có go để build agent" "Chạy scripts/setup.sh (menu mục 2) để cài Go"

# ---- build
version="$(git -C "$REPO_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"
name="scs-agent-$version-linux-$ARCH"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
pkg="$work/scs-agent"
mkdir -p "$pkg/bin" "$pkg/scripts" "$pkg/deploy/systemd"

echo "  Build scs-agent $version (linux/$ARCH)..."
(cd "$AGENT_SRC" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" "$GO" build -trimpath \
	-ldflags "-s -w -X main.version=$version" -o "$pkg/bin/scs-agent" ./cmd/scs-agent)
chmod 755 "$pkg/bin/scs-agent"
ok "Đã build binary ($(du -h "$pkg/bin/scs-agent" | cut -f1))"

# ---- ghép gói
install -m 755 "$AGENT_SRC/scripts/install.sh" "$pkg/scripts/install.sh"
install -m 755 "$AGENT_SRC/menu.sh" "$pkg/menu.sh"
install -m 644 "$AGENT_SRC/deploy/systemd/scs-agent.service" "$pkg/deploy/systemd/scs-agent.service"
install -m 644 "$CA_FILE" "$pkg/agent-ca.pem"
cat >"$pkg/bundle.env" <<EOF
# Giá trị đóng sẵn khi tạo gói (scs-server: scripts/package-agent.sh). install.sh dùng làm mặc định.
SCS_SERVER=$SERVER
SCS_WATCH_SERVICES=$WATCH
SCS_AGENT_VERSION=$version
EOF
cat >"$pkg/HUONG-DAN.txt" <<EOF
SCS-Agent $version (linux/$ARCH) - gói cài cho máy tại điểm bán

Server: $SERVER

Cài (cần quyền root):
  sudo bash scs-agent/scripts/install.sh        cài luôn với địa chỉ server đóng sẵn
  sudo bash scs-agent/menu.sh                   hoặc dùng menu (kiểm tra trước, cài, xem trạng thái)

Trước khi cài nên đặt hostname đúng quy ước: <mã siêu thị>-TN1-PRI, -TN2-SEC, -TN3, -TN4...
  sudo hostnamectl set-hostname 12345-TN1-PRI

Cài xong script in MÃ KHÓA MÁY; admin vào web quản trị, trang "Chờ duyệt", đối chiếu mã rồi bấm Duyệt.
EOF

mkdir -p "$OUT_DIR"
# Chủ sở hữu ghi là 0 (root): gói không mang tên user của máy build.
tar -C "$work" --owner=0 --group=0 --numeric-owner -czf "$OUT_DIR/$name.tar.gz" scs-agent
(cd "$OUT_DIR" && sha256sum "$name.tar.gz" >"$name.tar.gz.sha256")
ok "Đã tạo gói: $OUT_DIR/$name.tar.gz"

cat <<EOF

  Gói    : $OUT_DIR/$name.tar.gz  ($(du -h "$OUT_DIR/$name.tar.gz" | cut -f1))
  SHA256 : $(cut -d' ' -f1 "$OUT_DIR/$name.tar.gz.sha256")
  Server : $SERVER

  Trên máy tại điểm bán:
    1. Chép file .tar.gz sang (USB, scp: scp $name.tar.gz <user>@<máy>:~)
    2. tar xzf $name.tar.gz
    3. sudo bash scs-agent/scripts/install.sh      (hoặc: sudo bash scs-agent/menu.sh)
EOF
