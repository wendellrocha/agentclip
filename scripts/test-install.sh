#!/bin/sh
# Tests the authenticity checks of scripts/install.sh without touching the
# network: curl and gh are replaced by stubs that serve local fixtures.
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
installer="$root/scripts/install.sh"
fixtures="$root/scripts/testdata"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT HUP INT TERM

failures=0
pass() { printf 'ok   %s\n' "$1"; }
fail() {
  printf 'FAIL %s\n' "$1"
  failures=$((failures + 1))
}
expect() { # expect <description> <command...>: passes when the command succeeds
  description="$1"
  shift
  if "$@"; then pass "$description"; else fail "$description"; fi
}

# --- 1. statement_matches against the real GitHub response for v0.7.1-rc.1 ---

real_digest="5232f1dfbfac70b8919c4dac7681441fa2ec0c05ec608ada2c7d9d9256a1ee71"
real_payload="$(tr -d '\n' <"$fixtures/attestation-v0.7.1-rc.1.json" \
  | grep -o '"payload"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | sed 's/.*:[[:space:]]*"\(.*\)"/\1/')"

matches() { # matches <digest> <repository> <version>
  (
    digest_under_test="$1" repository_under_test="$2" version_under_test="$3"
    # A sourced script inherits the positional parameters, which its own
    # option parsing would reject.
    set --
    export AGENTCLIP_INSTALLER_LIB=1
    # shellcheck disable=SC1090
    . "$installer"
    # The sourced installer reads these variables, which shellcheck cannot see.
    # shellcheck disable=SC2034
    actual_checksum="$digest_under_test" repository="$repository_under_test" requested_version="$version_under_test"
    printf '%s' "$real_payload" | decode_base64 | statement_matches
  )
}

expect "real payload matches its archive, repository and tag" matches "$real_digest" wendellrocha/agentclip v0.7.1-rc.1
if matches 0000000000000000000000000000000000000000000000000000000000000000 wendellrocha/agentclip v0.7.1-rc.1; then fail "real payload refused for another digest"; else pass "real payload refused for another digest"; fi
if matches "$real_digest" wendellrocha/agentclip v0.7.1; then fail "real payload refused for another tag"; else pass "real payload refused for another tag"; fi
if matches "$real_digest" attacker/agentclip v0.7.1-rc.1; then fail "real payload refused for another repository"; else pass "real payload refused for another repository"; fi

# --- 2. the whole installer with stubbed curl and gh ---

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in x86_64 | amd64) arch=amd64 ;; *) arch=arm64 ;; esac
version="v0.7.1"
asset="agentclip_${version}_${os}_${arch}.tar.gz"
repository="example/agentclip"

mkdir -p "$work/release/agentclip_${version}_${os}_${arch}" "$work/bin" "$work/tools"

# The installer runs with a PATH that holds only the tools it needs plus the
# stubs, so a real gh on this machine can never leak into a scenario.
for tool in sh env tar gzip awk uname mktemp rm mkdir install sed head tr grep cat cp mv chmod base64 sha256sum shasum openssl basename dirname ls wc sort cut; do
  found="$(command -v "$tool" 2>/dev/null || true)"
  case "$found" in /*) ln -s "$found" "$work/tools/$tool" ;; esac
done
printf '#!/bin/sh\necho %s\n' "$version" >"$work/release/agentclip_${version}_${os}_${arch}/agentclip"
chmod +x "$work/release/agentclip_${version}_${os}_${arch}/agentclip"
tar -C "$work/release" -czf "$work/archive.tar.gz" "agentclip_${version}_${os}_${arch}"
if command -v sha256sum >/dev/null 2>&1; then
  digest="$(sha256sum "$work/archive.tar.gz" | awk '{ print $1 }')"
else
  digest="$(shasum -a 256 "$work/archive.tar.gz" | awk '{ print $1 }')"
fi
printf '%s  %s\n' "$digest" "$asset" >"$work/checksums.txt"
printf 'gh bundle' >"$work/attestation.jsonl"

# api_body <digest> <ref> <repository> <path> writes a GitHub attestations response.
api_body() {
  statement="{\"subject\":[{\"name\":\"x\",\"digest\":{\"sha256\":\"$1\"}}],\"predicate\":{\"buildDefinition\":{\"externalParameters\":{\"workflow\":{\"ref\":\"$2\",\"repository\":\"https://github.com/$3\",\"path\":\"$4\"}}}}}"
  encoded="$(printf '%s' "$statement" | base64 | tr -d '\n')"
  printf '{"attestations":[{"bundle":{"dsseEnvelope":{"payload":"%s"}}}]}' "$encoded"
}

cat >"$work/bin/curl" <<'STUB'
#!/bin/sh
out=""
write_status=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  if [ "$prev" = "-w" ]; then write_status="$arg"; fi
  prev="$arg"
  url="$arg"
done
prev=""
serve() { cp "$1" "$out"; }
case "$url" in
  */attestation.jsonl)
    [ "${HAVE_BUNDLE:-1}" = "1" ] || exit 22
    serve "$WORK/attestation.jsonl"
    ;;
  */checksums.txt) serve "$WORK/checksums.txt" ;;
  */agentclip_*.tar.gz) serve "$WORK/archive.tar.gz" ;;
  https://api.github.com/*/attestations/sha256:*)
    echo "$url" >>"$WORK/api.log"
    [ "${API_UNREACHABLE:-0}" = "1" ] && exit 7
    if [ "${API_STATUS:-200}" = "200" ]; then cp "$WORK/api.json" "$out"; else echo '{}' >"$out"; fi
    [ -n "$write_status" ] && printf '%s' "${API_STATUS:-200}"
    exit 0
    ;;
  *) echo "curl stub: unexpected URL $url" >&2; exit 22 ;;
esac
STUB
cat >"$work/bin/gh" <<'STUB'
#!/bin/sh
echo "$*" >>"$WORK/gh.log"
case "$*" in
  "attestation verify --help") [ "${GH_SUPPORTS:-1}" = "1" ] ;;
  "attestation verify "*) exit "${GH_VERIFY_RC:-0}" ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$work/bin/curl" "$work/bin/gh"

# run_install <description> <expected exit> [VAR=value ...] runs a fresh install.
run_install() {
  description="$1"
  expected="$2"
  shift 2
  rm -rf "$work/install" "$work/api.log" "$work/gh.log"
  : >"$work/api.log"
  : >"$work/gh.log"
  set +e
  env WORK="$work" PATH="$work/bin:$work/tools" AGENTCLIP_REPOSITORY="$repository" AGENTCLIP_INSTALL_DIR="$work/install" "$@" \
    sh "$installer" --version "$version" >"$work/out.log" 2>&1
  code=$?
  set -e
  if [ "$code" = "$expected" ]; then pass "$description"; else
    fail "$description (exit $code, want $expected)"
    sed 's/^/     | /' "$work/out.log"
  fi
}
installed() { [ -x "$work/install/agentclip" ]; }
gh_ran_verify() { grep -q '^attestation verify /' "$work/gh.log"; }
api_was_queried() { [ -s "$work/api.log" ]; }
good_api() { api_body "$digest" "refs/tags/$version" "$repository" ".github/workflows/release.yml" >"$work/api.json"; }

good_api
run_install "gh present and accepting: installs" 0 GH_VERIFY_RC=0
expect "  the binary was installed" installed
expect "  gh verified the downloaded archive" gh_ran_verify
if api_was_queried; then fail "  the API is not consulted when gh succeeded"; else pass "  the API is not consulted when gh succeeded"; fi

run_install "gh rejecting the archive: refuses, even if the API would accept" 1 GH_VERIFY_RC=1
if installed; then fail "  nothing was installed"; else pass "  nothing was installed"; fi
if api_was_queried; then fail "  no fallback to the weaker API check"; else pass "  no fallback to the weaker API check"; fi

run_install "gh without attestation support: falls back to the API" 0 GH_SUPPORTS=0
expect "  the API was queried for the archive digest" grep -q "sha256:$digest" "$work/api.log"
expect "  the binary was installed" installed

run_install "release without the bundle asset: falls back to the API" 0 HAVE_BUNDLE=0
expect "  the API was queried" api_was_queried

rm -f "$work/bin/gh"
run_install "no gh installed and a matching attestation: installs" 0
expect "  the binary was installed" installed
expect "  the API was queried for the archive digest" grep -q "sha256:$digest" "$work/api.log"

api_body "$digest" "refs/heads/main" "$repository" ".github/workflows/release.yml" >"$work/api.json"
run_install "attestation made from a branch: refuses" 1
api_body "$digest" "refs/tags/v9.9.9" "$repository" ".github/workflows/release.yml" >"$work/api.json"
run_install "attestation made for another tag: refuses" 1
api_body "$digest" "refs/tags/$version" "attacker/agentclip" ".github/workflows/release.yml" >"$work/api.json"
run_install "attestation made in another repository: refuses" 1
api_body "$digest" "refs/tags/$version" "$repository" ".github/workflows/evil.yml" >"$work/api.json"
run_install "attestation made by another workflow: refuses" 1
api_body "1111111111111111111111111111111111111111111111111111111111111111" "refs/tags/$version" "$repository" ".github/workflows/release.yml" >"$work/api.json"
run_install "attestation covering another file: refuses" 1
if installed; then fail "  nothing was installed"; else pass "  nothing was installed"; fi

good_api
run_install "no attestation (HTTP 404): refuses" 1 API_STATUS=404
run_install "API rate limited (HTTP 403): refuses and explains the opt-out" 1 API_STATUS=403
expect "  the message mentions AGENTCLIP_SKIP_ATTESTATION" grep -q AGENTCLIP_SKIP_ATTESTATION "$work/out.log"
run_install "API unreachable: refuses" 1 API_UNREACHABLE=1

run_install "explicit opt-out installs with only the SHA-256 check" 0 API_STATUS=404 AGENTCLIP_SKIP_ATTESTATION=1
expect "  the opt-out was announced" grep -q 'AGENTCLIP_SKIP_ATTESTATION=1' "$work/out.log"
if api_was_queried; then fail "  the API is not consulted after the opt-out"; else pass "  the API is not consulted after the opt-out"; fi

# A release that predates attestations cannot have one.
version="v0.7.0"
asset="agentclip_${version}_${os}_${arch}.tar.gz"
mkdir -p "$work/release/agentclip_${version}_${os}_${arch}"
cp "$work/release/agentclip_v0.7.1_${os}_${arch}/agentclip" "$work/release/agentclip_${version}_${os}_${arch}/agentclip"
tar -C "$work/release" -czf "$work/archive.tar.gz" "agentclip_${version}_${os}_${arch}"
if command -v sha256sum >/dev/null 2>&1; then digest="$(sha256sum "$work/archive.tar.gz" | awk '{ print $1 }')"; else digest="$(shasum -a 256 "$work/archive.tar.gz" | awk '{ print $1 }')"; fi
printf '%s  %s\n' "$digest" "$asset" >"$work/checksums.txt"
run_install "release before v0.7.1-rc.1 installs with a warning" 0 API_STATUS=404
expect "  the warning says it predates attestations" grep -q 'anterior aos atestados' "$work/out.log"

# A checksum mismatch is still fatal, before any attestation is consulted.
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$asset" >"$work/checksums.txt"
run_install "SHA-256 mismatch: refuses" 1 AGENTCLIP_SKIP_ATTESTATION=1

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures check(s) failed" >&2
  exit 1
fi
echo "all installer checks passed"
