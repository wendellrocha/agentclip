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

Releases from v0.7.1-rc.1 on are verified against their GitHub build
attestation. AGENTCLIP_SKIP_ATTESTATION=1 installs with the SHA-256 check only.

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

attestation_min_version="v0.7.1-rc.1"

decode_base64() {
  if printf '' | base64 -d >/dev/null 2>&1; then
    base64 -d
  elif printf '' | base64 -D >/dev/null 2>&1; then
    base64 -D
  else
    openssl base64 -d -A
  fi
}

# statement_matches reads an in-toto statement on stdin and succeeds only when
# it covers the downloaded archive and was made by the release workflow of this
# repository on the tag being installed. Values never contain spaces, so all
# whitespace is dropped and the fields are compared literally.
statement_matches() {
  statement="$(tr -d ' \t\r\n')"
  workflow="$(printf '%s' "$statement" | sed -n 's/.*"workflow":{\([^}]*\)}.*/\1/p')"
  [ -n "$workflow" ] || return 1
  printf '%s' "$statement" | grep -Fq "\"sha256\":\"${actual_checksum}\"" || return 1
  printf '%s' "$workflow" | grep -Fq "\"repository\":\"https://github.com/${repository}\"" || return 1
  printf '%s' "$workflow" | grep -Fq "\"path\":\".github/workflows/release.yml\"" || return 1
  printf '%s' "$workflow" | grep -Fq "\"ref\":\"refs/tags/${requested_version}\"" || return 1
}

# Both verifiers return 0 when the archive is authentic, 1 when it is rejected
# and 2 when the method could not run, so the next one can be tried.
verify_with_gh() {
  command -v gh >/dev/null 2>&1 || return 2
  gh attestation verify --help >/dev/null 2>&1 || return 2
  curl -fsSL --retry 3 -o "$temporary_directory/attestation.jsonl" "$base_url/attestation.jsonl" 2>/dev/null || return 2
  echo "Verificando a autenticidade com gh attestation verify..."
  gh attestation verify "$temporary_directory/$asset" \
    --bundle "$temporary_directory/attestation.jsonl" \
    --repo "$repository" \
    --cert-identity "https://github.com/${repository}/.github/workflows/release.yml@refs/tags/${requested_version}" >/dev/null
}

verify_with_api() {
  echo "Verificando a autenticidade na API de atestados do GitHub..."
  api_status="$(curl -sS -o "$temporary_directory/attestations.json" -w '%{http_code}' \
    -H "Accept: application/vnd.github+json" -H "User-Agent: agentclip-installer" \
    "https://api.github.com/repos/${repository}/attestations/sha256:${actual_checksum}")" || {
    echo "Could not reach the GitHub attestations API." >&2
    return 2
  }
  case "$api_status" in
    200) ;;
    404)
      echo "GitHub has no build attestation for ${asset}." >&2
      return 1
      ;;
    *)
      echo "The GitHub attestations API returned HTTP ${api_status}." >&2
      return 2
      ;;
  esac
  for payload in $(tr -d '\n' < "$temporary_directory/attestations.json" \
    | grep -o '"payload"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | sed 's/.*:[[:space:]]*"\(.*\)"/\1/'); do
    if printf '%s' "$payload" | decode_base64 2>/dev/null | statement_matches; then
      return 0
    fi
  done
  echo "No GitHub attestation matches ${asset} for ${requested_version}." >&2
  return 1
}

verify_authenticity() {
  if [ "${AGENTCLIP_SKIP_ATTESTATION:-}" = "1" ]; then
    echo "Aviso: verificação de atestado ignorada (AGENTCLIP_SKIP_ATTESTATION=1); apenas o SHA-256 foi conferido." >&2
    return 0
  fi
  if [ "$(version_compare "$requested_version" "$attestation_min_version")" = "-1" ]; then
    echo "Aviso: ${requested_version} é anterior aos atestados de build; apenas o SHA-256 foi conferido." >&2
    return 0
  fi
  verify_with_gh
  case "$?" in
    0)
      echo "Autenticidade confirmada por gh attestation verify."
      return 0
      ;;
    1)
      echo "gh attestation verify rejected ${asset}." >&2
      return 1
      ;;
  esac
  verify_with_api
  case "$?" in
    0)
      echo "Autenticidade confirmada pela API de atestados do GitHub."
      echo "Para a verificação criptográfica completa, instale o gh e use gh attestation verify."
      return 0
      ;;
    1) return 1 ;;
  esac
  echo "Could not verify the build attestation. Set AGENTCLIP_SKIP_ATTESTATION=1 to install with the SHA-256 check only." >&2
  return 1
}

# Lets the tests load the functions above without installing anything.
if [ "${AGENTCLIP_INSTALLER_LIB:-}" = "1" ]; then
  return 0 2>/dev/null || exit 0
fi

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

verify_authenticity || {
  echo "Refusing to install ${asset}: its authenticity could not be confirmed." >&2
  exit 1
}

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
