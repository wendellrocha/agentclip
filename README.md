# AgentClip

**Português** · [English](README.en.md)

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
o SHA-256 e a autenticidade do build (veja abaixo) e inspecionam a versão já
instalada. Eles baixam e atualizam somente
quando a versão encontrada é mais nova; repetir o instalador é seguro. Para uma
versão específica, use `--version vX.Y.Z` no instalador POSIX ou
`-Version vX.Y.Z` no PowerShell. Os artefatos e checksums também estão nas
[GitHub Releases](https://github.com/wendellrocha/agentclip/releases).

### Verificar a autenticidade de um release

O `checksums.txt` de cada release é assinado com o
[cosign](https://github.com/sigstore/cosign) (assinatura keyless pela identidade
do workflow de release), e os artefatos têm atestado de proveniência do GitHub.
Para verificar manualmente, baixe o arquivo do release, o `checksums.txt` e o
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
# macOS (não tem sha256sum nem --ignore-missing)
ARCH=$(uname -m | sed s/x86_64/amd64/) # arm64 or amd64
grep " agentclip_${VERSION}_darwin_${ARCH}.tar.gz$" checksums.txt | shasum -a 256 --check

gh attestation verify "agentclip_${VERSION}_linux_amd64.tar.gz" \
  --repo wendellrocha/agentclip \
  --cert-identity "$IDENTITY"
```

A identidade exige o ref exato da tag do release, então uma execução do workflow
a partir de outra branch não é aceita.

O `gh attestation verify` sem `--bundle` consulta o GitHub e exige `gh auth login`.
Para verificar sem login, baixe também o `attestation.jsonl` da release e acrescente
`--bundle attestation.jsonl`.

### Verificação automática

A partir da `v0.7.1-rc.1`, os instaladores e o `agentclip upgrade` confirmam,
além do SHA-256, que o arquivo baixado foi produzido pelo workflow de release
deste repositório na tag que está sendo instalada. Se a confirmação falhar, a
instalação é recusada.

- **`install.sh` e `install.ps1`:** usam `gh attestation verify` com o
  `attestation.jsonl` da release, que dispensa `gh auth login`, quando o `gh`
  está instalado. Se o `gh` **rejeitar** o arquivo, a instalação falha, sem
  tentar um método mais fraco. Sem `gh`, ou se a release não tiver o bundle,
  consultam a API de atestados do GitHub.
- **`agentclip upgrade`:** consulta a API de atestados do GitHub.
- **Servidores (`agentclip setup` e `agentclip upgrade`):** esta máquina baixa o
  binário da plataforma do servidor, faz a mesma verificação do `upgrade` e o
  envia pela conexão SSH. Nenhum instalador é baixado para o servidor: ele só
  executa uma sequência curta e fixa de comandos que confere se o SHA-256 do que
  recebeu é o do binário verificado e move o arquivo para o lugar; um envio
  truncado nunca substitui o binário que já funciona. Um servidor que já tem a
  mesma versão, ou uma mais nova, não é alterado.

A consulta à API confirma que o repositório tem um atestado para exatamente o
SHA-256 do arquivo, feito pelo `release.yml` na tag da versão, o que impede a
troca de arquivos de uma release. Ela confia no TLS e na API do GitHub e **não
verifica a assinatura** em si. Para a verificação criptográfica completa, instale
o `gh` ou use os comandos manuais acima.

Se a API estiver fora do ar ou com o limite de requisições estourado, a
instalação é recusada em vez de seguir sem verificar. Para instalar mesmo assim,
apenas com o SHA-256, defina `AGENTCLIP_SKIP_ATTESTATION=1`. No `agentclip setup` e no
`agentclip upgrade` a variável vale também para os binários enviados aos
servidores. Versões anteriores
à `v0.7.1-rc.1` não têm atestado e são instaladas só com o SHA-256, com um aviso.

## Início rápido

Instale e configure o servidor em uma única chamada:

```bash
agentclip setup bastion-m2 --profile m2
```

O comando instala a versão compatível no servidor Linux/macOS via SSH, detecta
todos os harnesses suportados já instalados, registra automaticamente a
integração AgentClip em cada um deles, cria o perfil local e inicia o Companion.
Quando o SSH ainda aceita apenas senha, o `setup` pede essa senha uma vez para
instalar uma chave Ed25519 privada do AgentClip; os túneis seguintes não pedem
senha. Se uma chave SSH já autentica o destino, o AgentClip não a altera.
Em seguida:

```bash
ssh bastion-m2
codex
```

No agente, peça por exemplo: `Analise o arquivo CSV que está no meu clipboard.`

O Companion acompanha imagens, texto e arquivos copiados do gerenciador de
arquivos. O conteúdo só deixa o host quando uma ferramenta MCP é chamada.

Ele também pode receber um arquivo criado no servidor. Peça ao agente para
oferecer o arquivo ao host; a página do Companion exibirá nome, tamanho e
expiração para você aceitar ou recusar. A chamada do agente espera essa decisão
por até 10 minutos e, no aceite, retoma o fluxo para que o agente entregue o
arquivo pelo túnel. O arquivo é salvo numa inbox privada local. O servidor
nunca escolhe nem recebe o caminho absoluto de destino no host.

Na seção **Arquivos do servidor**, use **Abrir conteúdo** para revisar no
navegador arquivos recebidos de texto, código ou dados tabulares, como `.csv`,
`.tsv`, `.sql`, `.json`, `.yaml` e arquivos de código. **Copiar conteúdo** copia
o texto desses arquivos para a área de transferência. A página é local e
privada; o conteúdo é servido como texto simples, sem executar HTML. Formatos
binários, como `.xlsx` e imagens, exibem **Baixar** e **Copiar caminho**, mas
não as ações de conteúdo de texto.

Após atualizar para uma versão com esse recurso, refaça o pareamento do perfil
para propagar a capability de upload aos harnesses remotos:

```bash
agentclip setup bastion-m2 --profile m2
```

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

O Companion verifica a release estável no início e mostra um aviso na página
local e na ferramenta MCP `agentclip_update_status` quando houver atualização.
Para atualizar o executável local e todos os perfis remotos salvos, use:

```bash
agentclip upgrade
```

O comando valida o SHA-256 publicado e o atestado de build da release, mantém
parados os perfis que já estavam
parados e reinicia somente os Companions que estavam ativos.

`agentclip companion open m2` abre uma página web local, protegida por token,
com estado do túnel, itens e expiração do clipboard, último erro e um botão
para parar o Companion. Ela escuta somente em `127.0.0.1`. Veja todos os
detalhes em [Referência de comandos](COMMANDS.md#página-web-do-companion).

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
- Arquivos enviados do servidor para o host exigem aceite local, aceitam um
  arquivo regular de até 50 MiB por vez, têm SHA-256 verificado e são gravados
  atomicamente em `~/.cache/agentclip/received/` (ou o cache equivalente da
  plataforma). Ofertas expiram em 10 minutos e o Companion as esquece 30 minutos depois;
  os arquivos entregues ficam nessa pasta até você apagá-los.
- O servidor remoto precisa ser confiável: ele recebe o conteúdo somente após
  uma chamada explícita da ferramenta MCP.

## Documentação

- [Referência de comandos](COMMANDS.md): todos os comandos, opções, página web
  do Companion e comandos internos.
- [Desenvolvimento](DEVELOPMENT.md): build local, testes, validação de CSV e
  processo de release.
- [Segurança](SECURITY.md): como reportar vulnerabilidades e verificar releases.
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
