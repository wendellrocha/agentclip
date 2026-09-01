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

Os scripts detectam a plataforma, consultam a release mais recente, verificam
o SHA-256 e inspecionam a versão já instalada. Eles baixam e atualizam somente
quando a versão encontrada é mais nova; repetir o instalador é seguro. Para uma
versão específica, use `--version vX.Y.Z` no instalador POSIX ou
`-Version vX.Y.Z` no PowerShell. Os artefatos e checksums também estão nas
[GitHub Releases](https://github.com/wendellrocha/agentclip/releases).

## Início rápido

Instale e configure o servidor em uma única chamada:

```bash
agentclip setup bastion-m2 --profile m2
```

O comando instala a versão compatível no servidor Linux/macOS via SSH, detecta
todos os harnesses suportados já instalados, registra automaticamente a
integração AgentClip em cada um deles, cria o perfil local e inicia o Companion.
Em seguida:

```bash
ssh bastion-m2
codex
```

No agente, peça por exemplo: `Analise o arquivo CSV que está no meu clipboard.`

O Companion acompanha imagens, texto e arquivos copiados do gerenciador de
arquivos. O conteúdo só deixa o host quando uma ferramenta MCP é chamada.

## Companion, background e reinicializações

`agentclip setup` inicia o Companion local em background (salvo com
`--no-start`); `agentclip companion start <perfil>` faz o mesmo manualmente.
O processo acompanha o clipboard, mantém o bridge local e abre o túnel SSH
reverso para o destino salvo no perfil. A view e o estado atual podem ser
consultados sem prender o terminal:

```bash
agentclip companion status m2
agentclip companion open m2
agentclip companion stop m2
```

O perfil — destino SSH, porta remota e token de pareamento — fica salvo no
host. O processo em execução, o bridge e o túnel não: AgentClip **não instala
um serviço de inicialização automática** (LaunchAgent no macOS, systemd no
Linux ou Agendador de Tarefas no Windows), nem escolhe automaticamente o
último perfil após o login. Portanto, se o **host** for reiniciado, inicie o
perfil desejado novamente:

```bash
agentclip companion start m2
```

Se apenas o **servidor remoto** cair ou for reiniciado enquanto o Companion
local continua rodando, ele tenta restabelecer o túnel SSH automaticamente,
com espera progressiva de 1 a 30 segundos. Quando a conexão voltar, o mesmo
perfil e a mesma porta remota voltam a ser usados. Se quiser que o Companion
suba junto com o sistema, use o gerenciador de serviços do seu sistema
operacional para executar explicitamente `agentclip companion start <perfil>`
após o login; essa automação ainda não é configurada pelo AgentClip.

Por padrão, `setup`, `pair` e `connect` usam todos os harnesses suportados que
encontrarem. A instalação/registro da integração é automática: não é preciso
editar JSON nem criar a extensão do Pi manualmente. Use `--agent` para limitar
a configuração a um deles. Para remover apenas a integração do AgentClip de um
harness, use `uninstall`; o CLI/harness e o perfil local continuam intactos:

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
| Claude Code | Suportado | Detectado e configurado automaticamente quando instalado |
| Gemini CLI | Suportado | Detectado e configurado automaticamente quando instalado |
| AGY / Antigravity CLI | Suportado | AgentClip cria/atualiza automaticamente o MCP global |
| OpenCode | Suportado | AgentClip cria/atualiza automaticamente o MCP local global |
| Pi Coding Agent | Suportado | AgentClip instala automaticamente uma extensão global |
| Outro cliente MCP | Manual | Configure `agentclip mcp` e as variáveis do perfil manualmente |

| Modelo ou provedor | Status direto | Como usar |
| --- | --- | --- |
| DeepSeek | Não aplicável | Use por meio de um harness compatível, como Pi ou OpenCode |
| MiMo | Não aplicável | Use por meio de um harness MCP; não há um adaptador de modelo direto |

Os adaptadores planejados nunca instalarão clientes de terceiros sem ação
explícita do usuário, nem gravarão o token do AgentClip em configurações de
projeto versionáveis.

Os adaptadores nunca instalam harnesses de terceiros. `setup`, `pair` e
`connect` detectam os seis CLIs suportados e configuram somente os que já estão
instalados; `--agent` permite escolher um deles.

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
arquivos. Os adaptadores automáticos atuais são Codex, Claude Code, Gemini CLI,
AGY / Antigravity CLI, OpenCode e Pi Coding Agent.

## Licença

Distribuído sob a [licença MIT](LICENSE).
