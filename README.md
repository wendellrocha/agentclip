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

O comando instala a versão compatível no servidor Linux/macOS via SSH, detecta
Codex, Claude Code e Gemini CLI instalados no servidor, configura o MCP em cada
um deles, cria o perfil local e inicia o Companion. Em seguida:

```bash
ssh bastion-m2
codex
```

No agente, peça por exemplo: `Analise o arquivo CSV que está no meu clipboard.`

O Companion acompanha imagens, texto e arquivos copiados do gerenciador de
arquivos. O conteúdo só deixa o host quando uma ferramenta MCP é chamada.

Por padrão, `setup`, `pair` e `connect` usam todos os harnesses suportados que
encontrarem. Use `--agent` para limitar a configuração a um deles. Para remover
apenas o MCP do AgentClip de um harness, use `uninstall`; o CLI/harness e o
perfil local continuam intactos:

```bash
agentclip setup bastion-m2 --profile m2 --agent claude
agentclip connect m2
agentclip uninstall m2 --agent gemini
```

## Compatibilidade de agentes

AgentClip integra-se ao **harness** — o programa que executa o agente e chama
ferramentas MCP —, não ao modelo escolhido dentro dele. A tabela separa o que
a versão atual configura automaticamente do que ainda é planejamento.

| Harness | Status | Integração |
| --- | --- | --- |
| Codex | Suportado | Detectado e configurado automaticamente quando instalado |
| Claude Code | Implementado — E2E pendente | Detectado e configurado automaticamente quando instalado |
| Gemini CLI | Implementado — E2E pendente | Detectado e configurado automaticamente quando instalado |
| AGY / Antigravity CLI | Planejado | MCP `stdio`; configuração JSON de usuário |
| OpenCode | Planejado | MCP `stdio`; configuração JSON compatível por versão |
| Pi Coding Agent | Planejado | Requer extensão AgentClip explícita |
| Outro cliente MCP | Manual | Configure `agentclip mcp` e as variáveis do perfil manualmente |

| Modelo ou provedor | Status direto | Como usar |
| --- | --- | --- |
| DeepSeek | Não aplicável | Use por meio de um harness compatível, como Pi ou OpenCode |
| MiMo | Não aplicável | Use por meio de um harness MCP; não há um adaptador de modelo direto |

Os adaptadores planejados nunca instalarão clientes de terceiros sem ação
explícita do usuário, nem gravarão o token do AgentClip em configurações de
projeto versionáveis.

Antes de uma nova release, cada adaptador precisa validar imagem, texto e CSV
em um servidor remoto com o respectivo CLI instalado.

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
arquivos. Os adaptadores automáticos atuais são Codex, Claude Code e Gemini
CLI; AGY, OpenCode e Pi continuam planejados.

## Licença

Distribuído sob a [licença MIT](LICENSE).
