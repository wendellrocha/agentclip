# Política de segurança

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

## Escopo

Estão no escopo: autenticação do bridge e do Companion, túnel SSH, inbox de
arquivos recebidos, instaladores e o processo de upgrade. Consulte também as
áreas sensíveis em [CONTRIBUTING.md](CONTRIBUTING.md).
