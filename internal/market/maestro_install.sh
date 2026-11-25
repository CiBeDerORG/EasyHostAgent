# 此脚本随 Agent 编译，参数由 Agent 固定为本机 Homebrew 前缀和操作记录目录。
set -euo pipefail
prefix="$1"
state_dir="$2"
brew="$prefix/bin/brew"
tap=mobile-dev-inc/tap
formula="$tap/maestro"

file_matches_sha256() {
    local actual
    actual="$(shasum -a 256 < "$1")" || return 1
    [[ "${actual%% *}" == "$2" ]]
}

prepare_maestro_archive() (
    "$brew" tap "$tap"
    local tap_dir remote
    tap_dir="$("$brew" --repo "$tap")"
    if ! remote="$(git -C "$tap_dir" config --get remote.origin.url)"; then
        echo 'Maestro 安装失败：无法读取本机 tap 的来源。' >&2
        return 1
    fi
    case "${remote%.git}" in
        https://github.com/mobile-dev-inc/homebrew-tap|git@github.com:mobile-dev-inc/homebrew-tap|ssh://git@github.com/mobile-dev-inc/homebrew-tap) ;;
        *) echo 'Maestro 安装失败：本机 tap 的来源与官方仓库不一致。' >&2; return 1 ;;
    esac
    if "$brew" command trust >/dev/null 2>&1; then
        "$brew" trust --formula "$formula"
    fi

    local metadata url expected cache_root key cache
    metadata="$("$brew" info --json=v2 --formula "$formula" | ruby -rjson -e '# encoding: UTF-8
        formula = JSON.parse(STDIN.read).fetch("formulae").first
        stable = formula && formula.dig("urls", "stable")
        url = stable && stable["url"]
        sha = stable && stable["checksum"]
        unless formula && formula["full_name"] == ARGV.first &&
               %r{\Ahttps://github\.com/mobile-dev-inc/maestro/releases/download/[A-Za-z0-9][A-Za-z0-9._+-]*/maestro\.zip\z}.match?(url.to_s) &&
               /\A[0-9a-fA-F]{64}\z/.match?(sha.to_s)
          abort "Maestro 安装失败：官方配方缺少有效的发布地址或 SHA-256。"
        end
        puts "#{url} #{sha.downcase}"
    ' "$formula")"
    read -r url expected <<< "$metadata"
    cache_root="$("$brew" --cache)"
    [[ "$cache_root" == /* ]] || { echo 'Maestro 安装失败：Homebrew 缓存路径无效。' >&2; return 1; }
    # Homebrew 按原 URL 的散列与文件名索引下载。先用缓存根目录，避免在探测文件名时先连接 GitHub。
    key="$(printf '%s' "$url" | shasum -a 256)"
    cache="$cache_root/downloads/${key%% *}--maestro.zip"
    mkdir -p "$cache_root/downloads"
    if [[ -L "$cache" || ( -e "$cache" && ! -f "$cache" ) ]]; then
        echo 'Maestro 安装失败：安装包的缓存位置不是普通文件。' >&2
        return 1
    fi
    if [[ -f "$cache" ]] && file_matches_sha256 "$cache" "$expected"; then
        return 0
    fi

    local -a mirrors=(gh-proxy.org ghfast.top ghproxy.net)
    local first=0 previous='' i host candidate temporary state_tmp
    local state="$state_dir/maestro-release-mirror.last"
    if [[ -f "$state" ]]; then
        read -r previous < "$state" || true
        for i in "${!mirrors[@]}"; do
            if [[ "${mirrors[i]}" == "$previous" ]]; then
                first=$(( (i + 1) % ${#mirrors[@]} ))
                break
            fi
        done
    fi
    temporary="$(mktemp "$cache_root/downloads/.maestro.XXXXXXXX")"
    state_tmp=''
    trap 'rm -f -- "$temporary" "$state_tmp"' EXIT
    state_tmp="$(mktemp "$state_dir/.maestro-mirror.XXXXXXXX")"
    printf '%s\n' "${mirrors[first]}" > "$state_tmp"
    mv -f "$state_tmp" "$state"

    for (( i=0; i<=${#mirrors[@]}; i++ )); do
        if (( i < ${#mirrors[@]} )); then
            host="${mirrors[(first + i) % ${#mirrors[@]}]}"
            candidate="https://$host/$url"
        else
            host=github.com
            candidate="$url"
        fi
        printf 'Maestro 下载：尝试 %s\n' "$host" >&2
        if ! curl -q -fLsS --proto '=https' --proto-redir '=https' --connect-timeout 8 \
                --max-time 600 --speed-limit 1024 --speed-time 30 -o "$temporary" "$candidate"; then
            printf 'Maestro 下载失败：%s 连接或传输失败。\n' "$host" >&2
            continue
        fi
        if ! file_matches_sha256 "$temporary" "$expected"; then
            printf 'Maestro 下载失败：%s 返回的文件与官方 SHA-256 不一致。\n' "$host" >&2
            continue
        fi
        mv -f "$temporary" "$cache"
        printf 'Maestro 下载完成：%s，SHA-256 校验通过。\n' "$host" >&2
        return 0
    done
    echo 'Maestro 下载失败：三个加速站和 GitHub 官方源均未取得校验通过的安装包。' >&2
    return 1
)

installed="$("$brew" list --formula --full-name)"
if ! grep -Fxq "$formula" <<< "$installed"; then
    if grep -Eq '(^|/)maestro$' <<< "$installed"; then
        echo 'Maestro 安装失败：发现其他来源的同名配方。' >&2
        exit 1
    fi
    prepare_maestro_archive
    # 安装使用刚核验的同一份配方，仍由 Homebrew 处理依赖、收据和 CLI 链接。
    HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 "$brew" install --formula "$formula"
fi
if ! command -v adb >/dev/null; then
    HOMEBREW_NO_INSTALL_CLEANUP=1 "$brew" install --cask android-platform-tools
fi
adb version
if [[ ! "$prefix/bin/maestro" -ef "$prefix/opt/maestro/bin/maestro" ]]; then
    echo 'Maestro 安装失败：已链接的 CLI 与 Homebrew 配方不一致。' >&2
    exit 1
fi
jdk_prefix="$("$brew" --prefix openjdk)"
JAVA_HOME="$jdk_prefix/libexec/openjdk.jdk/Contents/Home" "$prefix/bin/maestro" --version
