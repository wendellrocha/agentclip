# Política de segurança

**Português** · [English](#security-policy)

O AgentClip move conteúdo privado (clipboard e arquivos) entre a máquina local e
um servidor remoto. Levamos relatos de segurança a sério.

## Como reportar uma vulnerabilidade

Use o reporte privado de vulnerabilidades do GitHub:
<https://github.com/wendellrocha/agentclip/security/advisories/new>

Não abra issues públicas, nem inclua tokens, conteúdo de clipboard ou arquivos
de servidores reais no relato. Descreva o impacto, os passos para reproduzir e
a versão afetada (`agentclip --version`).

Você pode esperar uma confirmação de recebimento em alguns dias. As correções
são publicadas em uma nova release e creditadas no advisory, se você quiser.

## Versões suportadas

Apenas a release mais recente recebe correções de segurança. Atualize com
`agentclip upgrade`.

## Verificação de releases

Os releases têm `checksums.txt` assinado com cosign e atestado de proveniência.
Os instaladores e o `agentclip upgrade` confirmam automaticamente o atestado a
partir da `v0.7.1-rc.1`. Veja "Verificar a autenticidade de um release" no
[README](README.md), que também descreve os limites dessa verificação.

Cada release também publica um SBOM CycloneDX por plataforma
(`agentclip_vX.Y.Z_<os>_<arch>.cdx.json`) com os módulos compilados naquele
binário, coberto pelos mesmos checksums e pelo atestado. O workflow de release roda o `govulncheck` em cada binário antes de
assinar e é interrompido se encontrar uma vulnerabilidade conhecida.

## Escopo

Estão no escopo: autenticação do bridge e do Companion, túnel SSH, inbox de
arquivos recebidos, instaladores e o processo de upgrade. Consulte também as
áreas sensíveis em [CONTRIBUTING.md](CONTRIBUTING.md).

---

# Security policy

AgentClip moves private content (the clipboard and files) between the local
machine and a remote server. We take security reports seriously.

## How to report a vulnerability

Use GitHub's private vulnerability reporting:
<https://github.com/wendellrocha/agentclip/security/advisories/new>

Please do not open public issues, and do not include tokens, clipboard content
or files from real servers in your report. Describe the impact, the steps to
reproduce and the affected version (`agentclip --version`).

You can expect an acknowledgement within a few days. Fixes are published in a
new release and credited in the advisory, if you want.

## Supported versions

Only the latest release receives security fixes. Update with
`agentclip upgrade`.

## Release verification

Releases carry a `checksums.txt` signed with cosign and a provenance
attestation. The installers and `agentclip upgrade` confirm the attestation
automatically from `v0.7.1-rc.1` on. See "Verifying the authenticity of a
release" in the [English README](README.en.md), which also describes the limits
of that verification.

Each release also publishes one CycloneDX SBOM per platform
(`agentclip_vX.Y.Z_<os>_<arch>.cdx.json`) listing the modules compiled into that
binary, covered by the same checksums and attestation. The release workflow scans every binary with `govulncheck`
before signing and stops if it finds a known vulnerability.

## Scope

In scope: authentication of the bridge and the Companion, the SSH tunnel, the
inbox of received files, the installers and the upgrade process. See also the
sensitive areas in [CONTRIBUTING.md](CONTRIBUTING.md) (in Portuguese).
