# AgentClip

AgentClip disponibiliza, sob autorização explícita, imagens, texto e arquivos
do clipboard do host a um agente de código rodando em um servidor SSH.

Ele mantém um bridge HTTP local, um túnel SSH reverso e um servidor MCP por
`stdio`. Assim, o fluxo normal continua simples: `ssh servidor`, abra o Codex
e peça para ele examinar o conteúdo do clipboard.

## Instalação

No macOS ou Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/wendellrocha/agentclip/main/scripts/install.sh | sh
```

No Windows, execute no PowerShell:

```powershell
irm https://raw.githubusercontent.com/wendellrocha/agentclip/main/scripts/install.ps1 | iex
```

Os scripts detectam a plataforma, verificam o SHA-256 da release e instalam o
binário no diretório do usuário. Para uma versão específica, use
`--version vX.Y.Z` no instalador POSIX. Os artefatos e checksums também estão
nas [GitHub Releases](https://github.com/wendellrocha/agentclip/releases).

## Início rápido

Instale e configure o servidor em uma única chamada:

```bash
agentclip setup bastion-m2 --profile m2
```

O comando instala a versão compatível no servidor Linux/macOS via SSH,
configura o MCP do Codex, cria o perfil local e inicia o Companion. Em seguida:

```bash
ssh bastion-m2
codex
```

No agente, peça por exemplo: `Analise o arquivo CSV que está no meu clipboard.`

O Companion acompanha imagens, texto e arquivos copiados do gerenciador de
arquivos. O conteúdo só deixa o host quando uma ferramenta MCP é chamada.

## Segurança e limites

- O bridge escuta somente em `127.0.0.1`; o servidor o alcança apenas pelo
  túnel SSH ativo.
- Imagem e texto expiram em 90 segundos; arquivos em 10 minutos. Todos são de
  uso único.
- São aceitos até 5 arquivos regulares, 50 MiB cada e 100 MiB por
  materialização. Diretórios e symlinks são rejeitados.
- O servidor remoto precisa ser confiável: ele recebe o conteúdo somente após
  uma chamada explícita da ferramenta MCP.

## Documentação

- [Desenvolvimento](DEVELOPMENT.md): build local, testes, validação de CSV e
  processo de release.
- [Contribuindo](CONTRIBUTING.md): escopo de contribuições, testes e pull
  requests.

## Estado atual

O MVP inclui snapshots de imagem, texto e arquivos regulares, bridge local
autenticado, sessão SSH reversa persistente, perfil pareado, dashboard do
Companion e ferramentas MCP para status, imagem, texto e materialização de
arquivos. Adaptadores automáticos para Claude Code e OpenCode ainda não fazem
parte desta etapa.

## Licença

Distribuído sob a [licença MIT](LICENSE).
