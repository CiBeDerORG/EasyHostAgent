#!/usr/bin/env bash
# EasyHostAgent native Linux/FnOS installer.
# No Docker invocation, data deletion, automatic rollback, or source compilation.
set -Eeuo pipefail

readonly REPO="CiBeDerORG/EasyHostAgent"
readonly SERVICE="easyhostagent.service"
readonly INSTALL_BIN="/usr/local/bin/easyhostagent"
readonly UNIT_PATH="/etc/systemd/system/easyhostagent.service"
readonly CONFIG_PATH="/etc/easyhostagent/agent.env"
readonly MARKER_PATH="/etc/easyhostagent/managed-by-installer"

usage() {
  cat <<'HELP'
EasyHostAgent installer (Linux/FnOS, systemd).

Usage:
  sudo bash scripts/install.sh --binary /absolute/path/easyhostagent
  sudo bash scripts/install.sh --release v1.2.3
  sudo bash scripts/install.sh --release latest

Options:
  --binary PATH             Install an already trusted, executable local binary.
  --release TAG             Download GitHub release binary + SHA-256 asset.
  --software-root PATH      Initial SOFTWARE_ROOT (ignored on subsequent installs).
  --data-dir PATH           Initial Agent data directory (ignored on subsequent installs).
  --log-level LEVEL         Initial log level: debug|info|warn|error.
  --no-start                Install unit but do not enable/start/restart it.
  --staging-root PATH       Test-only root prefix; requires absolute non-root path;
                            never calls systemctl, never touches real host paths.
  --help

The script never deletes user files or Docker data. There is NO automated rollback.
The current repository has no formal GitHub Release; use --binary with the
GitHub Actions test artifact until a checksummed release is published.
HELP
}

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
say() { printf '[EasyHostAgent] %s\n' "$*"; }

source_binary=""
release=""
software_root=""
data_dir=""
log_level="info"
staging_root=""
no_start=0

while (( $# > 0 )); do
  case "$1" in
    --binary|--release|--software-root|--data-dir|--log-level|--staging-root)
      (( $# >= 2 )) || fail "$1 requires a value"
      case "$1" in
        --binary) source_binary="$2" ;;
        --release) release="$2" ;;
        --software-root) software_root="$2" ;;
        --data-dir) data_dir="$2" ;;
        --log-level) log_level="$2" ;;
        --staging-root) staging_root="$2" ;;
      esac
      shift 2 ;;
    --no-start) no_start=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

if [[ -n "$source_binary" && -n "$release" ]] || [[ -z "$source_binary" && -z "$release" ]]; then
  fail "choose exactly one of --binary or --release"
fi

if [[ "$(uname -s)" != "Linux" ]]; then fail "this version supports Linux/FnOS only"; fi
case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) fail "unsupported CPU architecture: $(uname -m)" ;;
esac
command -v sha256sum >/dev/null || fail "sha256sum is required"
command -v install >/dev/null || fail "install is required"
command -v mktemp >/dev/null || fail "mktemp is required"

valid_absolute() {
  local value="$1"
  [[ "$value" == /* && "$value" != "/" && "$value" != *$'\n'* &&
     "$value" != *$'\r'* && "$value" =~ ^/[A-Za-z0-9/_:.-]+$ ]] ||
     fail "unsafe or non-absolute path: $value"
  [[ "/$value/" != *"/../"* && "/$value/" != *"/./"* ]] ||
     fail "non-normalized path: $value"
}

if [[ -n "$staging_root" ]]; then
  valid_absolute "$staging_root"
  [[ "$staging_root" != *"/.." && "$staging_root" != *"/." ]] ||
    fail "non-normalized staging root"
  [[ -d "$staging_root" && ! -L "$staging_root" ]] ||
    fail "staging root must already be a real directory"
else
  (( EUID == 0 )) || fail "run as root (sudo) for a real system installation"
  command -v systemctl >/dev/null || fail "systemctl not found; systemd is required"
  [[ -d /run/systemd/system ]] || fail "systemd is not the running init system"
fi

if [[ -n "$source_binary" ]]; then
  valid_absolute "$source_binary"
  [[ -f "$source_binary" && ! -L "$source_binary" && -r "$source_binary" && -x "$source_binary" ]] ||
    fail "local binary must be an executable regular file (not a symlink)"
else
  [[ "$release" == "latest" || "$release" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([-+.][0-9A-Za-z.-]+)?$ ]] ||
    fail "release must be latest or a version tag such as v1.2.3"
  command -v curl >/dev/null || fail "curl is required for release installation"
fi

case "$log_level" in
  debug|info|warn|error) ;;
  *) fail "invalid log level: $log_level" ;;
esac

system_path() { printf '%s%s' "$staging_root" "$1"; }
binary_path="$(system_path "$INSTALL_BIN")"
unit_path="$(system_path "$UNIT_PATH")"
config_path="$(system_path "$CONFIG_PATH")"
marker_path="$(system_path "$MARKER_PATH")"

if [[ -e "$unit_path" && ! -e "$marker_path" ]]; then
  fail "an unmanaged unit already exists: $unit_path; refusing to overwrite"
fi
if [[ -L "$unit_path" || -L "$config_path" || -L "$binary_path" || -L "$marker_path" ]]; then
  fail "managed installation targets must not be symlinks"
fi
if [[ -e "$binary_path" && ! -e "$marker_path" ]]; then
  fail "an unmanaged binary already exists: $binary_path; refusing to overwrite"
fi

# Existing .env and agent state are never overwritten, even with changed options.
if [[ -f "$config_path" ]]; then
  if [[ -n "$data_dir" || -n "$software_root" || "$log_level" != "info" ]]; then
    say "existing configuration preserved; edit $config_path explicitly to change it"
  fi
else
  if [[ -z "$data_dir" ]]; then data_dir="/var/lib/easyhostagent"; fi
  if [[ -z "$software_root" ]]; then
    software_root="/opt/software"
    if [[ -f /etc/fnos-release ]] ||
      { [[ -r /etc/os-release ]] && grep -Eqi '^(ID|ID_LIKE|NAME)=.*fnos' /etc/os-release; }; then
      software_root="/vol3/1000/software"
    fi
  fi
  valid_absolute "$data_dir"
  valid_absolute "$software_root"
  [[ "$data_dir" != "$software_root" ]] || fail "data dir and software root must differ"
fi

repo_dir="$(cd -- "$(dirname -- "$0")/.." && pwd)"
unit_source="$repo_dir/packaging/systemd/easyhostagent.service"
[[ -f "$unit_source" ]] || fail "missing packaged systemd unit: $unit_source"

scratch="$(mktemp -d)"
stage_binary=""
cleanup() {
  if [[ -n "$stage_binary" ]]; then rm -f -- "$stage_binary"; fi
  rm -rf -- "$scratch"
}
trap cleanup EXIT

if [[ -n "$release" ]]; then
  asset="easyhostagent-linux-$arch"
  if [[ "$release" == "latest" ]]; then
    base_url="https://github.com/$REPO/releases/latest/download"
  else
    base_url="https://github.com/$REPO/releases/download/$release"
  fi
  say "downloading GitHub release asset $asset"
  curl -fLsS --proto '=https' --proto-redir '=https' --tlsv1.2 \
    --connect-timeout 10 --max-time 180 -o "$scratch/$asset" "$base_url/$asset" ||
    fail "release binary download failed"
  curl -fLsS --proto '=https' --proto-redir '=https' --tlsv1.2 \
    --connect-timeout 10 --max-time 60 -o "$scratch/$asset.sha256" "$base_url/$asset.sha256" ||
    fail "release checksum download failed"
  count="$(wc -l < "$scratch/$asset.sha256")"
  [[ "$count" -eq 1 ]] || fail "checksum asset must have exactly one line"
  checksum_line="$(cat "$scratch/$asset.sha256")"
  if [[ ! "$checksum_line" =~ ^([0-9a-fA-F]{64})[[:space:]][[:space:]]$asset$ ]]; then
    fail "invalid SHA-256 checksum asset format"
  fi
  expected="$(cut -d' ' -f1 "$scratch/$asset.sha256" | tr '[:upper:]' '[:lower:]')"
  actual="$(sha256sum "$scratch/$asset" | cut -d' ' -f1)"
  [[ "$actual" == "$expected" ]] || fail "downloaded binary SHA-256 mismatch"
  source_binary="$scratch/$asset"
  chmod 0755 "$source_binary"
fi

# Preflight the candidate before installing it, without changing live files.
"$source_binary" -version >/dev/null ||
  fail "candidate binary cannot execute on linux/$arch (or -version failed)"

install -d -m 0755 "$(dirname "$binary_path")" "$(dirname "$unit_path")"
install -d -m 0700 "$(dirname "$config_path")"

if [[ ! -e "$config_path" ]]; then
  config_tmp="$(mktemp "$(dirname "$config_path")/.easyhostagent.env.XXXXXX")"
  chmod 0600 "$config_tmp"
  printf 'EASYHOSTAGENT_DATA_DIR=%s\nEASYHOSTAGENT_SOFTWARE_ROOT=%s\nEASYHOSTAGENT_LOG_LEVEL=%s\n' \
    "$data_dir" "$software_root" "$log_level" > "$config_tmp"
  mv -f -- "$config_tmp" "$config_path"
  say "created initial config at $config_path"
fi

# Never source/eval existing EnvironmentFile contents as shell code.
effective_data="$(sed -n 's/^EASYHOSTAGENT_DATA_DIR=//p' "$config_path" | tail -n 1)"
valid_absolute "$effective_data"
install -d -m 0700 "$(system_path "$effective_data")"

stage_binary="$(mktemp "$(dirname "$binary_path")/.easyhostagent.bin.XXXXXX")"
install -m 0755 "$source_binary" "$stage_binary"
mv -f -- "$stage_binary" "$binary_path"
stage_binary=""

install -m 0644 "$unit_source" "$unit_path"
printf 'installed-by=EasyHostAgent\n' > "$marker_path"
chmod 0600 "$marker_path"

if [[ -n "$staging_root" ]]; then
  say "staged linux/$arch binary and unit under $staging_root; systemctl not invoked"
  exit 0
fi
systemctl daemon-reload
if (( no_start == 0 )); then
  systemctl enable "$SERVICE"
  systemctl restart "$SERVICE" ||
    fail "service failed to start; binary/config/data left in place; run journalctl -u $SERVICE"
  systemctl is-active --quiet "$SERVICE" ||
    fail "service did not reach active status; inspect journalctl -u $SERVICE"
  say "$SERVICE started successfully"
else
  say "unit installed; service was not enabled or started (--no-start)"
fi
say "data and existing application directories preserved"
