# 此脚本随 Agent 编译，仅接受 Agent 选定的本机 Homebrew 前缀。
set -euo pipefail
prefix="$1"
brew="$prefix/bin/brew"
formula=mobile-dev-inc/tap/maestro
installed="$("$brew" list --formula --full-name)"
if ! grep -Fxq "$formula" <<< "$installed"; then
    if grep -Eq '(^|/)maestro$' <<< "$installed"; then
        echo 'Maestro 安装失败：发现其他来源的同名配方。' >&2
        exit 1
    fi
    HOMEBREW_NO_INSTALL_CLEANUP=1 "$brew" install --formula "$formula"
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
