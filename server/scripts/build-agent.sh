#!/usr/bin/env bash
# build-agent.sh - build bản agent (linux amd64 + arm64) đi kèm bản server, vào server/bin/agent/:
#   VERSION, scs-agent-linux-amd64, scs-agent-linux-arm64
# install.sh chép thư mục này sang /opt/scs-server/agent; scs-server nhập vào kho phát hành lúc
# khởi động. update.sh và install.sh tự gọi script này.
#
#   bash scripts/build-agent.sh <phiên bản>

# Chạy bằng sh (dash) thì tự chuyển sang bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Eeuo pipefail

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGENT_SRC="$(cd "$SERVER_DIR/../agent" && pwd)"
OUT="$SERVER_DIR/bin/agent"
version="${1:-}"
[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$ ]] || { echo "build-agent.sh: phiên bản \"$version\" không hợp lệ" >&2; exit 2; }
GO="$(command -v go || echo /usr/local/go/bin/go)"
[[ -x "$GO" ]] || { echo "build-agent.sh: không có go" >&2; exit 1; }

mkdir -p "$OUT"
rm -f "$OUT"/scs-agent-linux-* "$OUT/VERSION"
for arch in amd64 arm64; do
	# -buildvcs=false: chạy bằng root trên repo của user khác thì git từ chối ("dubious ownership").
	(cd "$AGENT_SRC" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$GO" build -buildvcs=false -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$OUT/scs-agent-linux-$arch" ./cmd/scs-agent)
done
printf '%s\n' "$version" >"$OUT/VERSION"
echo "  ✔ Đã build agent $version (linux amd64, arm64) vào $OUT"
