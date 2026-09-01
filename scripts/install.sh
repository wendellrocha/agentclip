#!/bin/sh
# Install the latest AgentClip release on macOS or Linux.
set -eu

repository="${AGENTCLIP_REPOSITORY:-wendellrocha/agentclip}"
install_dir="${AGENTCLIP_INSTALL_DIR:-$HOME/.local/bin}"
requested_version="${AGENTCLIP_VERSION:-latest}"

usage() {
  cat <<'EOF'
Usage: install.sh [--version vX.Y.Z] [--install-dir PATH]

Environment overrides: AGENTCLIP_REPOSITORY, AGENTCLIP_VERSION,
AGENTCLIP_INSTALL_DIR.

Without --version, the installer checks the latest release and downloads it
only when it is newer than the installed AgentClip binary.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      requested_version="${2:?--version requires a value}"
      shift 2
      ;;
    --install-dir)
      install_dir="${2:?--install-dir requires a value}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "AgentClip installer requires '$1'." >&2
    exit 1
  }
}

require curl
require tar
require awk

version_compare() {
  # Prints 1 when the first semantic version is newer, -1 when it is older,
  # and 0 when both versions are equivalent. Build metadata is ignored.
  awk -v left="$1" -v right="$2" '
    function parse(value, target,    parts, core, identifiers) {
      sub(/^v/, "", value)
      sub(/\+.*/, "", value)
      split(value, parts, "-")
      core = parts[1]
      split(core, target, ".")
      prerelease = ""
      if (length(value) > length(core)) prerelease = substr(value, length(core) + 2)
      return prerelease
    }
    function compare_prerelease(left, right,    a, b, count, left_count, right_count, position, left_number, right_number) {
      if (left == "" && right == "") return 0
      if (left == "") return 1
      if (right == "") return -1
      left_count = split(left, a, ".")
      count = left_count
      right_count = split(right, b, ".")
      if (right_count > count) count = right_count
      for (position = 1; position <= count; position++) {
        if (position > left_count) return -1
        if (position > right_count) return 1
        left_number = a[position] ~ /^[0-9]+$/
        right_number = b[position] ~ /^[0-9]+$/
        if (left_number && right_number) {
          if ((a[position] + 0) > (b[position] + 0)) return 1
          if ((a[position] + 0) < (b[position] + 0)) return -1
        } else if (left_number) return -1
        else if (right_number) return 1
        else if (a[position] > b[position]) return 1
        else if (a[position] < b[position]) return -1
      }
      return 0
    }
    BEGIN {
      left_pre = parse(left, left_parts)
      right_pre = parse(right, right_parts)
      for (position = 1; position <= 3; position++) {
        if ((left_parts[position] + 0) > (right_parts[position] + 0)) { print 1; exit }
        if ((left_parts[position] + 0) < (right_parts[position] + 0)) { print -1; exit }
      }
      print compare_prerelease(left_pre, right_pre)
    }
  '
}

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *)
    echo "Unsupported operating system: $(uname -s). Use scripts/install.ps1 on Windows." >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "Unsupported CPU architecture: $(uname -m)." >&2
    exit 1
    ;;
esac

if [ "$requested_version" = "latest" ]; then
  echo "Buscando a versão mais recente do AgentClip..."
  requested_version="$(curl -fsSL -H "User-Agent: agentclip-installer" "https://api.github.com/repos/${repository}/releases/latest" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
    | head -n 1)"
else
  echo "Versão solicitada: ${requested_version}"
fi

case "$requested_version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *)
    echo "Could not resolve a semantic release tag (got '${requested_version:-empty}')." >&2
    exit 1
    ;;
esac

echo "Versão encontrada: ${requested_version}"

installed_binary="$install_dir/agentclip"
installed_version=""
if [ -x "$installed_binary" ]; then
  installed_version="$("$installed_binary" version 2>/dev/null || true)"
  case "$installed_version" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    [0-9]*.[0-9]*.[0-9]*) installed_version="v$installed_version" ;;
    *) installed_version="" ;;
  esac
fi

if [ -n "$installed_version" ]; then
  echo "Versão instalada encontrada: ${installed_version}"
  comparison="$(version_compare "$requested_version" "$installed_version")"
  case "$comparison" in
    0)
      echo "AgentClip ${installed_version} já está atualizado. Nenhum download necessário."
      exit 0
      ;;
    -1)
      echo "A versão instalada (${installed_version}) é mais nova que ${requested_version}. Nenhuma alteração realizada."
      exit 0
      ;;
    1)
      echo "Nova versão disponível: ${requested_version} (atual: ${installed_version})."
      ;;
    *)
      echo "Não foi possível comparar as versões ${installed_version} e ${requested_version}." >&2
      exit 1
      ;;
  esac
else
  echo "Nenhuma instalação válida foi encontrada em ${installed_binary}."
fi

asset="agentclip_${requested_version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${repository}/releases/download/${requested_version}"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

echo "Baixando AgentClip ${requested_version} para ${os}/${arch}..."
curl -fsSL --retry 3 -o "$temporary_directory/$asset" "$base_url/$asset"
curl -fsSL --retry 3 -o "$temporary_directory/checksums.txt" "$base_url/checksums.txt"

expected_checksum="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$temporary_directory/checksums.txt")"
if [ -z "$expected_checksum" ]; then
  echo "Checksum for ${asset} was not found in the release." >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum="$(sha256sum "$temporary_directory/$asset" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual_checksum="$(shasum -a 256 "$temporary_directory/$asset" | awk '{ print $1 }')"
else
  echo "AgentClip installer requires sha256sum or shasum to verify the release." >&2
  exit 1
fi

if [ "$expected_checksum" != "$actual_checksum" ]; then
  echo "Checksum mismatch for ${asset}; refusing to install it." >&2
  exit 1
fi

tar -xzf "$temporary_directory/$asset" -C "$temporary_directory"
binary="$temporary_directory/agentclip_${requested_version}_${os}_${arch}/agentclip"
if [ ! -f "$binary" ]; then
  echo "Release archive did not contain the expected AgentClip binary." >&2
  exit 1
fi

mkdir -p "$install_dir"
install -m 0755 "$binary" "$install_dir/agentclip"
if [ -n "$installed_version" ]; then
  echo "AgentClip atualizado: ${installed_version} → ${requested_version}."
else
  echo "AgentClip instalado: ${requested_version}."
fi
echo "Binário disponível em ${install_dir}/agentclip"

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    echo "Add ${install_dir} to PATH, then open a new terminal."
    ;;
esac
