#!/usr/bin/env bash
# setup.sh - chuẩn bị máy Ubuntu để chạy scs-server (môi trường dev/test).
#
# Các bước:
#   1. Quét điều kiện: Ubuntu, kiến trúc, sudo, dung lượng, RAM, đồng hồ, cổng
#   2. Cài gói: công cụ cơ bản, Docker + compose, Go bản chính thức (kiểm SHA256)
#   3. Kiểm tra mạng tới các nguồn tải (Go, Go modules, Docker Hub)
#   4. Tạo .env từ .env.example, sinh mật khẩu còn trống, chmod 600
#   5. Tùy chọn: khởi động Postgres + NATS (--start), chạy test (--test)
#
# Cách dùng (chạy bằng user thường, script tự gọi sudo khi cần):
#   ./scripts/setup.sh              quét + cài đặt
#   ./scripts/setup.sh --check      chỉ quét, không cài gì
#   ./scripts/setup.sh --start      cài xong khởi động Postgres + NATS
#   ./scripts/setup.sh --test       cài xong chạy go test
#   ./scripts/setup.sh --yes        không hỏi xác nhận
#   GO_VERSION=1.27.0 ./scripts/setup.sh   chọn bản Go
#
# Chạy lại nhiều lần được: phần nào đã đủ sẽ bỏ qua. Log ghi vào server/setup.log.

# Chạy bằng sh (dash) thì tự chuyển sang bash: script dùng cú pháp của bash.
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi

set -Eeuo pipefail

GO_VERSION="${GO_VERSION:-1.27.0}"
GO_MIN="1.22"
UBUNTU_MIN="22.04"
MIN_DISK_GB=10
MIN_RAM_MB=2048
PORTS=(6222 7222 5432 4222 8222)
ENV_SECRETS=(SCS_DB_PASSWORD SCS_NATS_PASSWORD)  # SCS_ADMIN_TOKEN là tùy chọn, không tự sinh

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$SERVER_DIR/.." && pwd)"
LOG_FILE="$SERVER_DIR/setup.log"
COMPOSE_FILE="$SERVER_DIR/compose.dev.yml"
TARGET_USER="${SUDO_USER:-$(id -un)}"

MODE_CHECK=0 OPT_START=0 OPT_TEST=0 OPT_YES=0
NEED_RELOGIN=0 NEED_DOCKER=0 NEED_GO=0
ARCH="" SUDO=""
WARNINGS=() ERRORS=() TODO=() MISSING_BASE=()

# ---------------------------------------------------------------- hiển thị
if [[ -t 1 ]]; then
	C_RED=$'\e[31m' C_GRN=$'\e[32m' C_YEL=$'\e[33m' C_BLU=$'\e[36m' C_B=$'\e[1m' C_0=$'\e[0m'
else
	C_RED='' C_GRN='' C_YEL='' C_BLU='' C_B='' C_0=''
fi

step() { printf '\n%s==> %s%s\n' "$C_B$C_BLU" "$*" "$C_0"; }
info() { printf '    %s\n' "$*"; }
ok()   { printf '  %s✔%s %s\n' "$C_GRN" "$C_0" "$*"; }
warn() { printf '  %s!%s %s\n' "$C_YEL" "$C_0" "$*"; WARNINGS+=("$*"); }
err()  { printf '  %s✖%s %s\n' "$C_RED" "$C_0" "$*"; ERRORS+=("$*"); }

# die: lỗi chặn, dừng ngay kèm gợi ý cách xử lý.
die() {
	printf '\n%s✖ LỖI: %s%s\n' "$C_RED$C_B" "$1" "$C_0" >&2
	[[ $# -gt 1 ]] && printf '  Cách xử lý: %s\n' "$2" >&2
	printf '  Log đầy đủ: %s\n' "$LOG_FILE" >&2
	exit 1
}

# Lỗi không lường trước (lệnh nào đó trả mã khác 0): báo dòng và lệnh hỏng.
on_error() {
	local code=$1 line=$2 cmd=$3
	printf '\n%s✖ LỖI không mong đợi ở dòng %s (mã %s)%s\n' "$C_RED$C_B" "$line" "$code" "$C_0" >&2
	printf '  Lệnh: %s\n  Log đầy đủ: %s\n' "$cmd" "$LOG_FILE" >&2
	exit "$code"
}
trap 'on_error $? $LINENO "$BASH_COMMAND"' ERR

usage() { sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

# ---------------------------------------------------------------- tiện ích
have() { command -v "$1" >/dev/null 2>&1; }

# version_ge A B: đúng nếu A >= B (so theo phiên bản, 1.10 > 1.9).
version_ge() { [[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" == "$2" ]]; }

go_bin() {
	if [[ -x /usr/local/go/bin/go ]]; then echo /usr/local/go/bin/go
	elif have go; then command -v go
	fi
}

go_version() {
	local g; g="$(go_bin)"
	[[ -n "$g" ]] || return 0
	"$g" version 2>/dev/null | awk '{print $3}' | sed 's/^go//' || true
}

# url_ok URL [mã HTTP chấp nhận thêm]: kiểm tra tải được (Docker Hub trả 401 là bình thường).
url_ok() {
	local code
	code="$(curl -s -o /dev/null -m 15 -w '%{http_code}' "$1" 2>/dev/null || true)"
	[[ "$code" =~ ^(2|3)[0-9][0-9]$ || ( -n "${2:-}" && "$code" == "$2" ) ]]
}

# env_value KEY: đọc giá trị KEY trong .env (dòng cuối cùng thắng), bỏ chú thích và ngoặc.
env_value() {
	local line val
	line="$(grep -E "^(export[[:space:]]+)?$1=" "$SERVER_DIR/.env" | tail -n1 || true)"
	val="${line#*=}"
	val="$(sed -E 's/^[[:space:]]*//; s/^#.*//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//' <<<"$val")"
	val="${val#\"}"; val="${val%\"}"; val="${val#\'}"; val="${val%\'}"
	printf '%s' "$val"
}

as_target_user() {
	if [[ $EUID -eq 0 && "$TARGET_USER" != root ]]; then
		sudo -u "$TARGET_USER" -H env PATH="/usr/local/go/bin:$PATH" "$@"
	else
		env PATH="/usr/local/go/bin:$PATH" "$@"
	fi
}

# ---------------------------------------------------------------- tham số
while [[ $# -gt 0 ]]; do
	case "$1" in
		--check) MODE_CHECK=1 ;;
		--start) OPT_START=1 ;;
		--test)  OPT_TEST=1 ;;
		--yes|-y) OPT_YES=1 ;;
		-h|--help) usage; exit 0 ;;
		*) printf 'Tham số không hợp lệ: %s\n\n' "$1" >&2; usage; exit 2 ;;
	esac
	shift
done

touch "$LOG_FILE" 2>/dev/null || die "Không ghi được $LOG_FILE" "Kiểm tra quyền ghi thư mục $SERVER_DIR"
exec > >(tee -a "$LOG_FILE") 2>&1
printf '\n===== setup.sh %s (user %s) =====\n' "$(date '+%F %T')" "$TARGET_USER"

# ---------------------------------------------------------------- 1. quét điều kiện
check_system() {
	step "1. Quét điều kiện hệ thống"

	[[ -r /etc/os-release ]] || die "Không xác định được hệ điều hành" "Script chỉ hỗ trợ Ubuntu $UBUNTU_MIN trở lên"
	# shellcheck disable=SC1091
	. /etc/os-release
	if [[ "${ID:-}" != ubuntu ]]; then
		die "Hệ điều hành là '${PRETTY_NAME:-không rõ}', không phải Ubuntu" "Dùng Ubuntu $UBUNTU_MIN hoặc 24.04 LTS"
	fi
	if ! version_ge "${VERSION_ID:-0}" "$UBUNTU_MIN"; then
		die "Ubuntu ${VERSION_ID:-?} quá cũ" "Nâng lên Ubuntu $UBUNTU_MIN trở lên (khuyên dùng 24.04 LTS)"
	fi
	ok "Hệ điều hành: $PRETTY_NAME"

	ARCH="$(dpkg --print-architecture)"
	case "$ARCH" in
		amd64|arm64) ok "Kiến trúc: $ARCH" ;;
		*) die "Kiến trúc $ARCH không được hỗ trợ" "Cần máy amd64 hoặc arm64" ;;
	esac

	if [[ $EUID -eq 0 ]]; then
		SUDO=""
		ok "Đang chạy bằng root"
	elif have sudo; then
		SUDO="sudo"
		if [[ $MODE_CHECK -eq 0 ]]; then
			info "Cần quyền sudo để cài gói (có thể hỏi mật khẩu của $TARGET_USER):"
			sudo -v || die "Không lấy được quyền sudo" "Chạy bằng user có quyền sudo, hoặc nhờ quản trị thêm vào nhóm sudo"
		fi
		ok "Có sudo"
	else
		die "Không có sudo và không phải root" "Chạy bằng root hoặc cài sudo"
	fi

	local disk_gb
	disk_gb="$(df -BG --output=avail /var/lib 2>/dev/null | tail -n1 | tr -dc '0-9' || true)"
	if [[ -z "$disk_gb" ]]; then
		warn "Không đọc được dung lượng trống của /var/lib"
	elif (( disk_gb < MIN_DISK_GB )); then
		err "Dung lượng trống /var/lib còn ${disk_gb}GB, cần ít nhất ${MIN_DISK_GB}GB (Docker image + dữ liệu DB)"
	else
		ok "Dung lượng trống /var/lib: ${disk_gb}GB"
	fi

	local ram_mb
	ram_mb="$(awk '/^MemTotal:/ {print int($2/1024)}' /proc/meminfo)"
	if (( ram_mb < MIN_RAM_MB )); then
		warn "RAM ${ram_mb}MB, nên có ít nhất ${MIN_RAM_MB}MB để chạy Postgres + NATS + server"
	else
		ok "RAM: ${ram_mb}MB"
	fi

	# Đồng hồ lệch làm hỏng kiểm chứng chỉ TLS và kiểm timestamp gói tin (lệch > 5 phút bị từ chối).
	if have timedatectl; then
		if [[ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null || true)" == yes ]]; then
			ok "Đồng hồ đã đồng bộ NTP"
		else
			warn "Đồng hồ chưa đồng bộ NTP; bật bằng: sudo timedatectl set-ntp true"
		fi
	fi

	if have ss; then
		local p busy=()
		for p in "${PORTS[@]}"; do
			if ss -ltnH "sport = :$p" 2>/dev/null | grep -q .; then busy+=("$p"); fi
		done
		if (( ${#busy[@]} > 0 )); then
			warn "Cổng đang có chương trình dùng: ${busy[*]} (bỏ qua nếu là Postgres/NATS của compose.dev.yml đang chạy)"
		else
			ok "Các cổng ${PORTS[*]} đang trống"
		fi
	fi

	# Firewall: chỉ báo, không tự mở cổng (mở cho ai là quyết định của quản trị).
	if have ufw && [[ $MODE_CHECK -eq 0 || $EUID -eq 0 ]]; then
		local ufw_status
		ufw_status="$($SUDO ufw status 2>/dev/null || true)"
		if grep -q '^Status: active' <<<"$ufw_status"; then
			# 6222: agent ở các cửa hàng phải vào được.
			if grep -qE "^6222(/tcp)?[[:space:]]+ALLOW" <<<"$ufw_status"; then
				ok "ufw cho phép cổng 6222 (agent)"
			else
				warn "ufw đang chặn cổng 6222 (agent gửi dữ liệu); mở cho mạng cửa hàng, ví dụ: sudo ufw allow from 10.0.0.0/8 to any port 6222 proto tcp"
			fi
			# 7222: chỉ VPN / máy server; mở cho mọi nơi là sai cấu hình.
			if grep -qE "^7222(/tcp)?[[:space:]]+ALLOW[[:space:]]+(IN[[:space:]]+)?Anywhere" <<<"$ufw_status"; then
				warn "ufw đang mở cổng 7222 (web quản trị) cho MỌI IP; chỉ nên mở cho dải VPN: sudo ufw delete allow 7222 && sudo ufw allow from <dải-VPN> to any port 7222 proto tcp"
			else
				ok "ufw không mở cổng 7222 ra ngoài (web quản trị chỉ VPN/máy server)"
			fi
		else
			warn "ufw không bật; cổng 7222 chỉ được bảo vệ bởi SCS_ADMIN_ALLOW trong .env"
		fi
	fi

	[[ -f "$COMPOSE_FILE" ]] || die "Không thấy $COMPOSE_FILE" "Chạy script từ trong thư mục dự án scs-agent đầy đủ"
	[[ -f "$SERVER_DIR/.env.example" ]] || die "Không thấy $SERVER_DIR/.env.example" "Chép lại đầy đủ thư mục server"
	ok "Thư mục dự án: $REPO_DIR"
}

# ---------------------------------------------------------------- liệt kê việc cần làm
plan_installs() {
	step "2. Kiểm tra phần mềm"
	local pkg missing_base=()
	for pkg in ca-certificates curl openssl git make tar iproute2; do
		if dpkg -s "$pkg" >/dev/null 2>&1; then :; else missing_base+=("$pkg"); fi
	done
	if (( ${#missing_base[@]} > 0 )); then
		info "Thiếu gói cơ bản: ${missing_base[*]}"
		TODO+=("Cài gói cơ bản: ${missing_base[*]}")
	else
		ok "Đủ gói cơ bản"
	fi
	MISSING_BASE=("${missing_base[@]}")

	if have docker && docker compose version >/dev/null 2>&1; then
		ok "Docker $(docker --version | awk '{print $3}' | tr -d ,), compose $(docker compose version --short 2>/dev/null || echo '?')"
		NEED_DOCKER=0
	else
		info "Chưa có Docker hoặc plugin docker compose"
		TODO+=("Cài Docker + docker compose (gói Ubuntu)")
		NEED_DOCKER=1
	fi

	local gv; gv="$(go_version)"
	if [[ -n "$gv" ]] && version_ge "$gv" "$GO_MIN"; then
		ok "Go $gv ($(go_bin))"
		NEED_GO=0
	else
		info "Go ${gv:-chưa cài} (cần >= $GO_MIN)"
		TODO+=("Cài Go $GO_VERSION vào /usr/local/go (tải từ dl.google.com, kiểm SHA256)")
		NEED_GO=1
	fi
	if dpkg -s golang-go >/dev/null 2>&1; then
		warn "Có gói apt golang-go (thường cũ); /usr/local/go/bin sẽ được đặt trước trong PATH"
	fi

	if [[ -f "$SERVER_DIR/.env" ]]; then
		ok "Đã có .env"
	else
		TODO+=("Tạo .env từ .env.example, sinh mật khẩu ngẫu nhiên")
	fi
}

confirm() {
	(( ${#TODO[@]} > 0 )) || return 0
	printf '\n%sSẽ thực hiện:%s\n' "$C_B" "$C_0"
	printf '    - %s\n' "${TODO[@]}"
	[[ $OPT_YES -eq 1 ]] && return 0
	if [[ ! -t 0 ]]; then
		die "Không có terminal để hỏi xác nhận" "Thêm --yes để chạy không cần hỏi"
	fi
	local ans
	read -r -p "Tiếp tục? [y/N] " ans
	[[ "$ans" =~ ^[YyCc]$ ]] || die "Đã hủy theo yêu cầu"
}

# ---------------------------------------------------------------- 3. cài đặt
install_base() {
	(( ${#MISSING_BASE[@]} > 0 )) || return 0
	step "Cài gói cơ bản"
	apt_update
	$SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y "${MISSING_BASE[@]}" \
		|| die "Cài gói cơ bản thất bại" "Xem lỗi apt phía trên; thử: sudo apt-get -f install"
	ok "Đã cài: ${MISSING_BASE[*]}"
}

APT_UPDATED=0
apt_update() {
	[[ $APT_UPDATED -eq 1 ]] && return 0
	info "apt-get update..."
	if ! $SUDO apt-get update -q; then
		die "apt-get update thất bại (không tải được danh sách gói)" \
			"Kiểm tra mạng/DNS; nếu công ty dùng proxy, khai trong /etc/apt/apt.conf.d/95proxy: Acquire::http::Proxy \"http://proxy:port\";"
	fi
	APT_UPDATED=1
}

check_network() {
	step "3. Kiểm tra mạng tới nguồn tải"
	local name url extra failed=0
	while IFS='|' read -r name url extra; do
		if url_ok "$url" "$extra"; then
			ok "$name"
		else
			err "Không kết nối được $name ($url)"
			failed=1
		fi
	done <<-EOF
		Go $GO_VERSION (dl.google.com)|https://dl.google.com/go/go${GO_VERSION}.linux-${ARCH}.tar.gz.sha256
		Go modules (proxy.golang.org)|https://proxy.golang.org/
		Docker Hub (registry-1.docker.io)|https://registry-1.docker.io/v2/|401
	EOF
	if [[ $failed -eq 1 ]]; then
		info "Nếu công ty dùng proxy: export https_proxy=http://proxy:port rồi chạy lại;"
		info "Docker cần cấu hình proxy riêng trong /etc/systemd/system/docker.service.d/proxy.conf"
	fi
}

install_docker() {
	[[ $NEED_DOCKER -eq 1 ]] || return 0
	step "Cài Docker"
	apt_update
	local pkgs=(docker.io docker-compose-v2)
	# Máy đã cài docker-ce từ repo của Docker: chỉ thêm plugin compose tương ứng.
	if dpkg -s docker-ce >/dev/null 2>&1; then pkgs=(docker-compose-plugin); fi
	$SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y "${pkgs[@]}" \
		|| die "Cài Docker thất bại" "Xem lỗi apt phía trên; nếu xung đột gói, gỡ bản Docker cũ rồi chạy lại"
	$SUDO systemctl enable --now docker || die "Không khởi động được dịch vụ docker" "Xem: sudo journalctl -u docker -n 50"
	docker compose version >/dev/null 2>&1 || $SUDO docker compose version >/dev/null 2>&1 \
		|| die "Đã cài Docker nhưng không chạy được 'docker compose'" "Kiểm tra gói docker-compose-v2"
	ok "Docker: $($SUDO docker --version)"
}

setup_docker_group() {
	[[ "$TARGET_USER" == root ]] && return 0
	have docker || return 0
	if id -nG "$TARGET_USER" | tr ' ' '\n' | grep -qx docker; then
		return 0
	fi
	$SUDO usermod -aG docker "$TARGET_USER" || die "Không thêm được $TARGET_USER vào nhóm docker"
	NEED_RELOGIN=1
	ok "Đã thêm $TARGET_USER vào nhóm docker (có hiệu lực sau khi đăng nhập lại)"
}

install_go() {
	[[ $NEED_GO -eq 1 ]] || return 0
	step "Cài Go $GO_VERSION"
	local file="go${GO_VERSION}.linux-${ARCH}.tar.gz"
	local url="https://dl.google.com/go/$file"
	local tmp; tmp="$(mktemp -d)"

	info "Tải $url"
	curl -fSL --retry 3 --connect-timeout 15 -o "$tmp/$file" "$url" \
		|| die "Không tải được Go $GO_VERSION" "Kiểm tra mạng/proxy, hoặc chọn bản khác: GO_VERSION=1.26.0 ./scripts/setup.sh"
	local want
	want="$(curl -fsSL --retry 3 "$url.sha256" | tr -dc '0-9a-f' || true)"
	[[ ${#want} -eq 64 ]] || die "Không lấy được mã SHA256 chính thức của Go" "Thử lại sau, hoặc kiểm tra mạng tới dl.google.com"
	echo "$want  $tmp/$file" | sha256sum -c --status \
		|| die "File Go tải về KHÔNG khớp SHA256 (có thể bị hỏng hoặc bị sửa)" "Không cài; thử tải lại, kiểm tra proxy/mạng"
	ok "SHA256 khớp"

	if [[ -d /usr/local/go ]]; then
		$SUDO rm -rf /usr/local/go.old
		$SUDO mv /usr/local/go /usr/local/go.old
		info "Bản Go cũ chuyển sang /usr/local/go.old"
	fi
	$SUDO tar -C /usr/local -xzf "$tmp/$file" || die "Giải nén Go thất bại"
	rm -rf "$tmp"
	# shellcheck disable=SC2016 # cố ý ghi nguyên văn $PATH vào file profile
	echo 'export PATH=/usr/local/go/bin:$PATH' | $SUDO tee /etc/profile.d/scs-go.sh >/dev/null
	export PATH="/usr/local/go/bin:$PATH"
	[[ "$(go_version)" == "$GO_VERSION" ]] || die "Cài xong nhưng 'go version' không ra $GO_VERSION"
	ok "Go $(go_version) tại /usr/local/go (PATH khai trong /etc/profile.d/scs-go.sh)"
}

# ---------------------------------------------------------------- 4. .env
setup_env() {
	step "4. File cấu hình .env"
	local env="$SERVER_DIR/.env"
	if [[ ! -f "$env" ]]; then
		[[ $MODE_CHECK -eq 1 ]] && { warn "Chưa có .env (chạy không --check để tạo)"; return 0; }
		cp "$SERVER_DIR/.env.example" "$env"
		ok "Đã tạo .env từ .env.example"
	fi

	local key val generated=()
	for key in "${ENV_SECRETS[@]}"; do
		val="$(env_value "$key")"
		if [[ -n "$val" ]]; then continue; fi
		if [[ $MODE_CHECK -eq 1 ]]; then
			warn "$key trong .env đang trống"
			continue
		fi
		# Chỉ điền dòng đang trống (KEY= hoặc KEY=   # chú thích); không ghi đè giá trị đã có.
		val="$(new_secret)"
		if grep -qE "^$key=" "$env"; then
			sed -i -E "s|^$key=[[:space:]]*(#.*)?$|$key=$val|" "$env"
		else
			printf '%s=%s\n' "$key" "$val" >>"$env"
		fi
		[[ "$(env_value "$key")" == "$val" ]] || die "Không điền được $key vào .env" "Sửa tay dòng $key trong $env"
		generated+=("$key")
	done
	(( ${#generated[@]} > 0 )) && ok "Đã sinh giá trị ngẫu nhiên cho: ${generated[*]} (xem trong .env, không in ra đây)"

	# NATS đọc mật khẩu như cú pháp config: bắt đầu bằng số (ví dụ "9f3a...") thì NATS không khởi động.
	# Bản cũ của script có thể đã sinh mật khẩu như vậy: sinh lại (NATS không lưu mật khẩu ở đâu khác).
	val="$(env_value SCS_NATS_PASSWORD)"
	if [[ -n "$val" && ! "$val" =~ ^[A-Za-z][A-Za-z0-9]*$ ]]; then
		if [[ $MODE_CHECK -eq 1 ]]; then
			warn "SCS_NATS_PASSWORD phải bắt đầu bằng chữ cái và chỉ gồm chữ, số (chạy không --check để sinh lại)"
		else
			val="$(new_secret)"
			sed -i -E "s|^SCS_NATS_PASSWORD=.*$|SCS_NATS_PASSWORD=$val|" "$env"
			ok "Đã sinh lại SCS_NATS_PASSWORD (mật khẩu cũ bắt đầu bằng số, NATS không đọc được)"
		fi
	fi

	if [[ -z "$(env_value SCS_NATS_USER)" ]]; then
		err "SCS_NATS_USER trong .env đang trống; điền ví dụ: SCS_NATS_USER=scs"
	fi

	if [[ $MODE_CHECK -eq 0 ]]; then
		chmod 600 "$env"
		[[ $EUID -eq 0 && "$TARGET_USER" != root ]] && chown "$TARGET_USER:" "$env" "$LOG_FILE"
		ok ".env quyền 600, chủ sở hữu $(stat -c %U "$env")"
	else
		local perm; perm="$(stat -c %a "$env")"
		if [[ "$perm" == 600 ]]; then
			ok ".env quyền 600"
		else
			warn ".env đang quyền $perm, nên là 600 (chmod 600 .env)"
		fi
	fi
}

# new_secret: 48 ký tự chữ thường và số, luôn bắt đầu bằng chữ cái (NATS hiểu "9..." là số).
new_secret() { printf 's%s' "$(openssl rand -hex 24)"; }

# sync_installed_env: scs-server đã cài dùng bản sao cấu hình ở /etc/scs-server/scs-server.env.
# Mật khẩu DB/NATS ở đó phải khớp server/.env (compose dùng file này), nếu không scs-server
# báo "password authentication failed". Chép mật khẩu từ .env sang, giữ bản cũ .bak-<thời điểm>.
sync_installed_env() {
	local etc=/etc/scs-server/scs-server.env key val cur changed=0
	$SUDO test -f "$etc" || return 0
	for key in SCS_DB_PASSWORD SCS_NATS_PASSWORD; do
		val="$(env_value "$key")"
		[[ -n "$val" ]] || continue
		cur="$($SUDO sed -n "s/^$key=//p" "$etc" | tail -n1)"
		[[ "$cur" == "$val" ]] && continue
		[[ $changed -eq 0 ]] && $SUDO cp -p "$etc" "$etc.bak-$(date +%Y%m%d-%H%M%S)"
		if $SUDO grep -qE "^$key=" "$etc"; then
			$SUDO sed -i -E "s|^$key=.*$|$key=$val|" "$etc"
		else
			printf '%s=%s\n' "$key" "$val" | $SUDO tee -a "$etc" >/dev/null
		fi
		changed=1
	done
	[[ $changed -eq 1 ]] || return 0
	ok "Đã cập nhật mật khẩu DB/NATS trong $etc cho khớp .env"
	if systemctl is-active --quiet scs-server 2>/dev/null || systemctl is-failed --quiet scs-server 2>/dev/null; then
		$SUDO systemctl restart scs-server && ok "Đã khởi động lại scs-server" || warn "Không khởi động lại được scs-server (xem: journalctl -u scs-server -n 30)"
	fi
}

# ---------------------------------------------------------------- 5. tùy chọn
docker_cmd() {
	if docker info >/dev/null 2>&1; then docker "$@"; else $SUDO docker "$@"; fi
}

start_deps() {
	[[ $OPT_START -eq 1 ]] || return 0
	step "5. Khởi động Postgres + NATS"
	cd "$SERVER_DIR"
	if ! docker_cmd compose -f "$COMPOSE_FILE" up -d --wait --wait-timeout 180; then
		docker_cmd compose -f "$COMPOSE_FILE" ps || true
		docker_cmd compose -f "$COMPOSE_FILE" logs --tail 30 || true
		die "Postgres/NATS không lên được (xem trạng thái và log phía trên)" \
			"Không tải được image: kiểm tra mạng tới Docker Hub/proxy. Sai mật khẩu sau khi đổi .env: docker compose -f compose.dev.yml down -v"
	fi
	docker_cmd compose -f "$COMPOSE_FILE" ps
	ok "Postgres và NATS đang chạy (healthy)"
	sync_installed_env
}

# test_db_url: Postgres (compose) đang chạy thì tạo DB riêng scs_test cho test cấu trúc DB,
# in chuỗi kết nối. Không có Postgres thì in rỗng (các test cần DB tự bỏ qua).
test_db_url() {
	local user pass port
	docker_cmd compose -f "$COMPOSE_FILE" ps --status running -q postgres 2>/dev/null | grep -q . || return 0
	user="$(env_value SCS_DB_USER)"; user="${user:-scs}"
	pass="$(env_value SCS_DB_PASSWORD)"
	port="$(env_value SCS_DB_PORT)"; port="${port:-5432}"
	if ! docker_cmd compose -f "$COMPOSE_FILE" exec -T postgres psql -U "$user" -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = 'scs_test'" | grep -q 1; then
		docker_cmd compose -f "$COMPOSE_FILE" exec -T postgres createdb -U "$user" scs_test >&2 || return 0
	fi
	# Mật khẩu do script sinh chỉ gồm chữ số hex nên ghép thẳng vào URL được.
	printf 'postgres://%s:%s@127.0.0.1:%s/scs_test?sslmode=disable' "$user" "$pass" "$port"
}

run_tests() {
	[[ $OPT_TEST -eq 1 ]] || return 0
	step "Chạy test"
	cd "$SERVER_DIR"
	local db_url
	db_url="$(test_db_url)"
	if [[ -n "$db_url" ]]; then
		ok "Có Postgres: chạy cả test cấu trúc DB trên DB riêng scs_test"
	else
		warn "Postgres chưa chạy: bỏ qua test cấu trúc DB (chạy kèm --start để test đủ)"
	fi
	if ! as_target_user env SCS_TEST_DATABASE_URL="$db_url" go test ../common/... ./...; then
		die "Test thất bại" "Xem lỗi phía trên; lỗi tải module thì kiểm tra mạng tới proxy.golang.org"
	fi
	ok "Test qua"
}

# ---------------------------------------------------------------- tổng kết
summary() {
	step "Tổng kết"
	printf '    %-10s %s\n' "Go" "$(go_version || true)"
	printf '    %-10s %s\n' "Docker" "$( (have docker && docker --version | awk '{print $3}' | tr -d ,) || echo 'chưa có')"
	printf '    %-10s %s\n' "Compose" "$( (have docker && docker compose version --short 2>/dev/null) || echo 'chưa có')"

	if (( ${#WARNINGS[@]} > 0 )); then
		printf '\n  %sCảnh báo (%d):%s\n' "$C_YEL" "${#WARNINGS[@]}" "$C_0"
		printf '    - %s\n' "${WARNINGS[@]}"
	fi
	if (( ${#ERRORS[@]} > 0 )); then
		printf '\n  %sLỗi cần xử lý (%d):%s\n' "$C_RED" "${#ERRORS[@]}" "$C_0"
		printf '    - %s\n' "${ERRORS[@]}"
	fi

	if [[ $MODE_CHECK -eq 1 ]]; then
		if (( ${#TODO[@]} > 0 )); then
			printf '\n  Chạy không có --check sẽ:\n'
			printf '    - %s\n' "${TODO[@]}"
		fi
	else
		printf '\n  %sBước tiếp theo:%s\n' "$C_B" "$C_0"
		[[ $NEED_RELOGIN -eq 1 ]] && info "- Đăng xuất rồi đăng nhập lại (hoặc: newgrp docker) để dùng docker không cần sudo"
		info "- Mở terminal mới (hoặc: source /etc/profile.d/scs-go.sh) để có lệnh go"
		[[ $OPT_START -eq 0 ]] && info "- cd $SERVER_DIR && docker compose -f compose.dev.yml up -d --wait"
		info "- cd $SERVER_DIR && SCS_INGEST_LISTEN=127.0.0.1:6222 go run ./cmd/scs-server all"
		info "  (chưa có PKI nên cổng agent chỉ chạy ở loopback; cần thử từ máy khác thì đặt SCS_DEV_INSECURE=1 trong .env, prod cấm)"
		info "- curl http://127.0.0.1:6222/readyz   và   curl -k https://127.0.0.1:7222/readyz"
		info "- Vào web: https://<IP-server>:7222 (qua VPN hoặc trên máy server), lấy mã bằng: cd $SERVER_DIR && go run ./cmd/scs-server login-code"
		info "- Chạy lâu dài, tự chạy khi khởi động lại máy: sudo bash $SERVER_DIR/scripts/install.sh --wan-ip <IP WAN>"
	fi
	printf '\n  Log: %s\n' "$LOG_FILE"

	if (( ${#ERRORS[@]} > 0 )); then
		printf '\n%s✖ Có %d lỗi, xem danh sách phía trên.%s\n' "$C_RED$C_B" "${#ERRORS[@]}" "$C_0"
		exit 1
	fi
	printf '\n%s✔ Hoàn tất.%s\n' "$C_GRN$C_B" "$C_0"
}

# ---------------------------------------------------------------- chạy
check_system
plan_installs

if [[ $MODE_CHECK -eq 1 ]]; then
	if have curl; then check_network; else warn "Chưa có curl, bỏ qua kiểm tra mạng"; fi
	setup_env
	summary
	exit 0
fi

(( ${#ERRORS[@]} == 0 )) || die "Điều kiện hệ thống chưa đạt: ${ERRORS[*]}" "Xử lý các lỗi trên rồi chạy lại"
confirm
install_base
check_network
(( ${#ERRORS[@]} == 0 )) || die "Không kết nối được nguồn tải" "Xem các dòng ✖ ở bước 3 và gợi ý proxy"
install_docker
setup_docker_group
install_go
setup_env
start_deps
run_tests
summary
