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
  requested_version="$(curl -fsSL -H "User-Agent: agentclip-installer" "https://api.github.com/repos/${repository}/releases/latest" \
    | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' \
    | head -n 1)"
fi

case "$requested_version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *)
    echo "Could not resolve a semantic release tag (got '${requested_version:-empty}')." >&2
    exit 1
    ;;
esac

asset="agentclip_${requested_version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${repository}/releases/download/${requested_version}"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

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
echo "Installed AgentClip ${requested_version} at ${install_dir}/agentclip"
"$install_dir/agentclip" version

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    echo "Add ${install_dir} to PATH, then open a new terminal."
    ;;
esac
