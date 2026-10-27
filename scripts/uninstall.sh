#!/usr/bin/env bash
# Remove only the EasyHostAgent binary/service, never managed apps or user data.
set -Eeuo pipefail

usage() {
  cat <<'HELP'
Usage: sudo bash scripts/uninstall.sh [--staging-root ABS_PATH]

Removes only installer-owned /usr/local/bin/easyhostagent and the systemd
unit. Always retains /etc/easyhostagent, /var/lib/easyhostagent, the
configured SOFTWARE_ROOT, Docker containers and volumes.
--staging-root stages the same removal in an isolated test directory; it never
calls systemctl. No data-purge option is provided.
HELP
}
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

staging_root=""
while (( $# > 0 )); do
  case "$1" in
    --staging-root)
      (( $# >= 2 )) || fail "--staging-root requires an argument"
      staging_root="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

if [[ -n "$staging_root" ]]; then
  [[ "$staging_root" == /* && "$staging_root" != "/" &&
     "$staging_root" =~ ^/[A-Za-z0-9/_:.-]+$ &&
     -d "$staging_root" && ! -L "$staging_root" ]] ||
    fail "staging root must be an existing absolute directory"
else
  (( EUID == 0 )) || fail "run as root using sudo"
  command -v systemctl >/dev/null || fail "systemctl required"
  [[ -d /run/systemd/system ]] || fail "systemd is not running"
fi

binary="$staging_root/usr/local/bin/easyhostagent"
unit="$staging_root/etc/systemd/system/easyhostagent.service"
marker="$staging_root/etc/easyhostagent/managed-by-installer"

[[ -f "$marker" && ! -L "$marker" ]] ||
  fail "refusing to uninstall: installation marker not found"
grep -qx 'installed-by=EasyHostAgent' "$marker" ||
  fail "installation marker unrecognized"
[[ ! -L "$unit" && ! -L "$binary" ]] ||
  fail "managed paths must not be symlinks"

if [[ -z "$staging_root" ]]; then
  # If this step fails, leave binaries/files for manual diagnosis.
  systemctl disable --now easyhostagent.service ||
    fail "could not stop service; files and data left in place"
fi
rm -f -- "$unit" "$binary" "$marker"
if [[ -z "$staging_root" ]]; then
  systemctl daemon-reload
fi
printf '[EasyHostAgent] service and binary removed; config, Agent identity, Docker apps and data retained\n'
