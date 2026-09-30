# AgentClip

[Português](README.md) · **English**

AgentClip makes images, text and files from the host's clipboard available,
with explicit authorization, to a coding agent running on an SSH server.

It runs a local HTTP bridge, a reverse SSH tunnel and an MCP server over
`stdio`. The everyday flow stays simple: `ssh server`, open Codex and ask it to
look at what is on your clipboard.

> The rest of the documentation (commands, development, contributing) is in
> Portuguese for now. The commands and options themselves are the same.

## Installation

On macOS or Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/wendellrocha/agentclip/main/scripts/install.sh | sh
```

On Windows, run in PowerShell:

```powershell
irm https://raw.githubusercontent.com/wendellrocha/agentclip/main/scripts/install.ps1 | iex
```

The scripts detect the platform, look up the latest release, verify its SHA-256
and the authenticity of the build (see below) and inspect the version that is
already installed. They download and update only when the version found is
newer; running the installer again is safe. For a specific version, use
`--version vX.Y.Z` with the POSIX installer or `-Version vX.Y.Z` with
PowerShell. Artifacts and checksums are also on
[GitHub Releases](https://github.com/wendellrocha/agentclip/releases).

### Verifying the authenticity of a release

Each release's `checksums.txt` is signed with
[cosign](https://github.com/sigstore/cosign) (keyless, using the identity of the
release workflow), and the artifacts carry a GitHub provenance attestation. To
verify by hand, download the release file, `checksums.txt` and
`checksums.txt.bundle`:

```bash
VERSION=vX.Y.Z
IDENTITY="https://github.com/wendellrocha/agentclip/.github/workflows/release.yml@refs/tags/${VERSION}"

cosign verify-blob \
  --bundle checksums.txt.bundle \
  --certificate-identity "$IDENTITY" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# Linux
sha256sum --check --ignore-missing checksums.txt
# macOS (it has neither sha256sum nor --ignore-missing)
ARCH=$(uname -m | sed s/x86_64/amd64/) # arm64 or amd64
grep " agentclip_${VERSION}_darwin_${ARCH}.tar.gz$" checksums.txt | shasum -a 256 --check

gh attestation verify "agentclip_${VERSION}_linux_amd64.tar.gz" \
  --repo wendellrocha/agentclip \
  --cert-identity "$IDENTITY"
```

The identity requires the exact ref of the release tag, so a run of the workflow
from any other branch is not accepted.

`gh attestation verify` without `--bundle` queries GitHub and requires
`gh auth login`. To verify without logging in, also download the release's
`attestation.jsonl` and add `--bundle attestation.jsonl`.

### Automatic verification

From `v0.7.1-rc.1` on, the installers and `agentclip upgrade` confirm, besides
the SHA-256, that the downloaded file was produced by this repository's release
workflow on the tag being installed. If the confirmation fails, the
installation is refused.

- **`install.sh` and `install.ps1`:** when `gh` is installed they use
  `gh attestation verify` with the release's `attestation.jsonl`, which needs no
  `gh auth login`. If `gh` **rejects** the file, the installation fails, without
  falling back to a weaker method. Without `gh`, or if the release has no
  bundle, they query the GitHub attestations API.
- **`agentclip upgrade`:** queries the GitHub attestations API.
- **Servers (`agentclip setup` and `agentclip upgrade`):** this machine downloads
  the binary for the server's platform, verifies it the way `upgrade` does and
  sends it over the SSH connection. No installer is downloaded to the server: it
  only runs a short, fixed sequence of commands that checks the SHA-256 of what
  it received against the verified binary's and moves the file into place; a
  truncated transfer never replaces the binary that already works. A server that
  already has the same version, or a newer one, is left unchanged.

The API lookup confirms that the repository holds an attestation for exactly the
file's SHA-256, made by `release.yml` on the version's tag, which stops release
files from being swapped. It trusts TLS and the GitHub API and **does not verify
the signature** itself. For the full cryptographic verification, install `gh` or
use the manual commands above.

If the API is down or its rate limit is exhausted, the installation is refused
instead of proceeding unverified. To install anyway, with the SHA-256 check
only, set `AGENTCLIP_SKIP_ATTESTATION=1`. With `agentclip setup` and
`agentclip upgrade` the variable also applies to the binaries sent to servers.
Versions before `v0.7.1-rc.1` have
no attestation and are installed with the SHA-256 only, with a warning.

## Quick start

Install and configure a server in a single call:

```bash
agentclip setup bastion-m2 --profile m2
```

The command installs the matching version on the Linux/macOS server over SSH,
detects every supported harness that is already installed, registers the
AgentClip integration with each of them automatically, creates the local
profile and starts the Companion. When SSH still accepts only a password,
`setup` asks for it once to install a private AgentClip Ed25519 key; later
tunnels do not ask for a password. If an SSH key already authenticates the
destination, AgentClip leaves it alone. Then:

```bash
ssh bastion-m2
codex
```

In the agent, ask for example: `Analyze the CSV file on my clipboard.`

The Companion follows images, text and files copied from the file manager. The
content leaves the host only when an MCP tool is called.

It can also receive a file created on the server. Ask the agent to offer the
file to the host; the Companion page shows its name, size and expiry for you to
accept or refuse. The agent's call waits for that decision for up to 10 minutes
and, once accepted, resumes so the agent can deliver the file through the
tunnel. The file is saved in a private local inbox. The server never chooses,
nor is it told, the absolute destination path on the host.

In the **Received files** section, use **Open content** to review text, code or
tabular files you received, such as `.csv`, `.tsv`, `.sql`, `.json`, `.yaml` and
source code. **Copy content** copies the text of those files to the clipboard.
The page is local and private; content is served as plain text, without running
HTML. Binary formats, such as `.xlsx` and images, show **Download** and **Copy
path**, but not the text content actions.

After updating to a version with this feature, pair the profile again to
propagate the upload capability to the remote harnesses:

```bash
agentclip setup bastion-m2 --profile m2
```

## Companion, background and restarts

`agentclip setup` starts the local Companion in the background (unless you pass
`--no-start`); `agentclip companion start <profile>` does the same by hand. The
process follows the clipboard, keeps the local bridge running and opens the
reverse SSH tunnel to the destination saved in the profile. The view and the
current state can be queried without tying up the terminal:

```bash
agentclip companion status m2
agentclip companion open m2
agentclip companion stop m2
```

The Companion checks for the stable release at startup and shows a notice on the
local page and in the `agentclip_update_status` MCP tool when an update exists.
To update the local executable and every saved remote profile, use:

```bash
agentclip upgrade
```

The command validates the published SHA-256 and the release's build
attestation, keeps stopped the profiles that were already stopped and restarts
only the Companions that were active.

`agentclip companion open m2` opens a local, token-protected web page with the
tunnel state, the clipboard items and their expiry, the last error and a button
to stop the Companion. It listens on `127.0.0.1` only. See all the details in the
[Command reference](COMMANDS.md#página-web-do-companion) (in Portuguese).

The profile — SSH destination, remote port and pairing token — is saved on the
host. The running process, the bridge and the tunnel are not. By default
AgentClip **does not install an automatic startup service**, so if the **host**
restarts, start the profile you want again:

```bash
agentclip companion start m2
```

To have the Companion start by itself when you log in, turn on autostart for the
profile. AgentClip uses what each system already has: a LaunchAgent on macOS, a
systemd user unit on Linux and a Task Scheduler task on Windows.

```bash
agentclip companion autostart enable m2
agentclip companion autostart status m2
agentclip companion autostart disable m2
```

Turning it off removes the service and stops the Companion it started; one you
started yourself keeps running. If the Companion is already running when the
login starts the service, the service does not open a second one.

If only the **remote server** goes down or restarts while the local Companion
keeps running, it tries to re-establish the SSH tunnel automatically, backing
off progressively from 1 to 30 seconds (starting over at 1 second after a
tunnel that stayed up for at least a minute). When the connection returns, the same
profile and the same remote port are used again.

By default, `setup`, `pair` and `connect` use every supported harness they find.
Installing and registering the integration is automatic: you do not need to
edit JSON or create the Pi extension by hand. Use `--agent` to limit the
configuration to one of them. To remove only the AgentClip integration from a
harness, use `uninstall`; the harness CLI and the local profile stay intact:

```bash
agentclip setup bastion-m2 --profile m2 --agent claude
agentclip connect m2
agentclip uninstall m2 --agent gemini
```

## Agent compatibility

AgentClip integrates with the **harness** — the program that runs the agent and
calls MCP tools — not with the model chosen inside it. The table separates what
the current version configures automatically from what is still planned.

| Harness | Status | Integration |
| --- | --- | --- |
| Codex | Supported | Detected and configured automatically when installed |
| Claude Code | Supported | Detected and configured automatically when installed |
| Gemini CLI | Supported | Detected and configured automatically when installed |
| AGY / Antigravity CLI | Supported | AgentClip creates/updates the global MCP automatically |
| OpenCode | Supported | AgentClip creates/updates the global local MCP automatically |
| Pi Coding Agent | Supported | AgentClip installs a global extension automatically |
| Another MCP client | Manual | Configure `agentclip mcp` and the profile variables by hand |

| Model or provider | Direct status | How to use it |
| --- | --- | --- |
| DeepSeek | Not applicable | Use it through a compatible harness, such as Pi or OpenCode |
| MiMo | Not applicable | Use it through an MCP harness; there is no direct model adapter |

Planned adapters will never install third-party clients without an explicit user
action, nor write the AgentClip token into project settings that could be
committed.

Adapters never install third-party harnesses. `setup`, `pair` and `connect`
detect the six supported CLIs and configure only those that are already
installed; `--agent` lets you choose one of them.

## Security and limits

- The bridge listens on `127.0.0.1` only; the server reaches it only through the
  active SSH tunnel.
- Image and text expire after 90 seconds; files after 10 minutes. All are
  single-use.
- Up to 5 regular files are accepted, 50 MiB each and 100 MiB per
  materialization. Directories and symlinks are rejected.
- Files sent from the server to the host require local approval, accept one
  regular file of up to 50 MiB at a time, have their SHA-256 verified and are
  written atomically to `~/.cache/agentclip/received/` (or the platform's
  equivalent cache). Offers expire after 10 minutes and the Companion forgets them
  30 minutes after that; delivered files stay in that folder until you delete
  them.
- The remote server must be trusted: it receives the content only after an
  explicit call to an MCP tool.

## Language

By default, command output, `agentclip help` and the Companion page are in
English. For Brazilian Portuguese set `AGENTCLIP_LANG=pt-BR` (it applies to the
installers too); the Companion page also accepts `?lang=pt-BR` and follows the
browser's language. Technical error messages stay in English. See
[Language](COMMANDS.md#idioma) in the command reference (in Portuguese).

```bash
AGENTCLIP_LANG=pt-BR agentclip help
```

## Documentation

- [Command reference](COMMANDS.md): every command, option, the Companion web
  page and the internal commands (in Portuguese).
- [Development](DEVELOPMENT.md): local build, tests, CSV validation and the
  release process (in Portuguese).
- [Security](SECURITY.md): how to report vulnerabilities and verify releases.
- [Contributing](CONTRIBUTING.md): the scope of contributions, tests and pull
  requests (in Portuguese).

## Current state

The MVP includes image, text and regular-file snapshots, an authenticated local
bridge, a persistent reverse SSH session, a paired profile, the Companion
dashboard and MCP tools for status, image, text and file materialization. The
current automatic adapters are Codex, Claude Code, Gemini CLI, AGY / Antigravity
CLI, OpenCode and Pi Coding Agent.

## License

Distributed under the [MIT license](LICENSE).
