# AgentClip

AgentClip disponibiliza, sob autorização explícita, o conteúdo do clipboard do
host a um agente de código que está rodando em um servidor SSH.

O MVP usa um bridge HTTP limitado a loopback, um encaminhamento SSH reverso e
um servidor MCP por `stdio`. O Companion mantém esse encaminhamento aberto em
segundo plano por perfil de servidor, portanto o fluxo normal é `ssh dev` e
depois `codex` — sem wrapper para abrir o agente.

## Estado atual

Este repositório contém snapshots de imagem, texto e arquivos regulares,
bridge local autenticado, sessão SSH reversa persistente, perfil pareado e
ferramentas MCP `clipboard_status`, `get_clipboard_image`,
`get_clipboard_text` e `materialize_clipboard_files`. O instalador automático
do binário remoto, o serviço de login e os adaptadores para Claude Code/OpenCode
ainda não fazem parte desta etapa.

## Instalação

No macOS ou Linux, instale a última release com um comando. O script detecta a
plataforma, verifica o SHA-256 publicado e instala em `~/.local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/wendellrocha/agentclip/main/scripts/install.sh | sh
```

No Windows, execute no PowerShell. Ele instala em
`%LOCALAPPDATA%\Programs\AgentClip\bin` e adiciona esse diretório ao `PATH` do
usuário:

```powershell
irm https://raw.githubusercontent.com/wendellrocha/agentclip/main/scripts/install.ps1 | iex
```

Também é possível fixar uma versão, para instalações reproduzíveis:

```bash
curl -fsSL https://raw.githubusercontent.com/wendellrocha/agentclip/main/scripts/install.sh | sh -s -- --version v0.1.0
```

Os arquivos compactados e `checksums.txt` continuam disponíveis na página de
[Releases](https://github.com/wendellrocha/agentclip/releases) para instalação
manual ou ambientes sem `curl`/PowerShell.

### Conectar um servidor

Após instalar localmente, um único comando instala a versão compatível no
servidor Linux/macOS via SSH, configura o MCP do Codex, cria o perfil e inicia
o Companion:

```bash
agentclip setup bastion-m2 --profile m2
```

Em uma release estável, `setup` instala no servidor a mesma versão do cliente.
Use `--version vX.Y.Z` para selecionar outra release. Para desenvolvimento sem
uma release publicada, mantenha o fluxo manual com `agentclip pair`.

### Build local

É necessário Go 1.27 ou superior.

```bash
go build -o ~/.local/bin/agentclip ./cmd/agentclip
```

Instale o mesmo binário no servidor, em um caminho acessível ao usuário que
executa o Codex. Durante o desenvolvimento, por exemplo:

```bash
scp ~/.local/bin/agentclip dev:~/.local/bin/agentclip
```

## Releases para mantenedores

Enviar uma tag semântica no formato `vX.Y.Z` executa os testes, gera os
artefatos multiplataforma e publica uma GitHub Release automaticamente:

```bash
git tag v0.1.0
git push origin v0.1.0
```

As notas são geradas pelo GitHub a partir dos pull requests desde a release
anterior. Para organizá-las, aplique labels como `feature`, `bug`, `security`,
`documentation`, `dependencies` ou `breaking-change`. Use
`ignore-for-release` ou `skip-changelog` para omitir um pull request. A
configuração está em [`.github/release.yml`](.github/release.yml).

## Companion (fluxo principal)

Instale um binário compatível também no servidor, em um caminho no `PATH` do
usuário remoto. O comando `pair` configura o MCP do Codex remotamente; ele
exige que tanto `agentclip` quanto `codex` já estejam instalados no servidor.

```bash
agentclip pair dev dev --remote-port 39123
agentclip companion start dev
agentclip companion open dev
```

`start` desacopla o Companion do terminal. A view é um dashboard local em
loopback, com estado do túnel, clipboard e um botão para parar o serviço; não
expõe tokens ou conteúdo do clipboard. Os comandos disponíveis são:

```bash
agentclip companion status dev
agentclip companion stop dev
agentclip companion run dev # modo foreground, útil para diagnóstico
```

Com o Companion iniciado, use o terminal normalmente:

```bash
ssh dev
codex
```

No agente, escreva algo como: `Veja o arquivo que está no meu clipboard.` O
Companion acompanha imagens, texto e arquivos copiados do gerenciador de
arquivos. O conteúdo permanece no host até uma ferramenta MCP ser chamada.
Para arquivos, ela devolve paths temporários privados no servidor para o agente
analisar localmente.

## Teste de arquivo/CSV

Atualize o binário nos dois lados antes de testar, pois o MCP roda o binário do
servidor:

```bash
# host
go build -o ./agentclip ./cmd/agentclip

# gere/copiar o binário da arquitetura do servidor, depois confirme:
ssh dev 'agentclip version'
```

Reinicie o Companion depois de atualizar o binário local:

```bash
./agentclip companion stop dev
./agentclip companion start dev
./agentclip companion open dev
```

Copie `vendas.csv` no Finder/Explorer. A view deve listar o nome do arquivo,
mas não seu conteúdo. Entre no servidor, abra uma nova sessão do Codex e diga:

```text
Analise o arquivo CSV que está no meu clipboard.
```

O agente deve consultar `clipboard_status` e chamar
`materialize_clipboard_files`; então recebe um caminho em
`~/.cache/agentclip/inbox/` no servidor. O caminho expira: arquivos ficam
disponíveis por 10 minutos antes da materialização, e o inbox é limpo em
inicializações/materializações posteriores após 30 minutos.

Para parear apenas o túnel (por exemplo, para configurar Claude Code ou
OpenCode manualmente), use `--skip-codex`. O MCP remoto deve executar
`agentclip mcp` com as variáveis `AGENTCLIP_BRIDGE_PORT` e
`AGENTCLIP_SESSION_TOKEN` do perfil. A automação dos adaptadores é próxima
etapa.

O autostart no login via launchd, systemd-user e Windows ainda está planejado;
por enquanto, execute `agentclip companion start <perfil>` uma vez após login.

## Fluxo legado por sessão

No host gráfico, copie uma imagem e arme-a:

```bash
agentclip arm
```

Em seguida, abra o SSH através do wrapper:

```bash
agentclip ssh dev
```

No servidor, execute o Codex e peça para analisar a imagem do clipboard. O
agente deve chamar `get_clipboard_image`; a próxima chamada falhará porque a
concessão é de uso único.

Para verificar apenas o bridge local:

```bash
agentclip doctor
```

## Segurança e limitações

- O bridge escuta somente em `127.0.0.1`; o servidor só o alcança pelo túnel
  SSH ativo.
- O perfil local e o estado do bridge são criados com permissão `0600`. O
  perfil também contém um segredo persistente, que o Codex remoto precisa
  guardar na sua configuração MCP. Trate servidor e conta remota como
  confiáveis; não sincronize esses arquivos de configuração.
- Imagem e texto expiram em 90 segundos; arquivos expiram em 10 minutos. Todo
  item continua sendo de uso único. Copie-o novamente se expirar.
- Arquivos regulares aceitos têm até 50 MiB; são até 5 itens ou 100 MiB por
  materialização. Diretórios e symlinks são rejeitados.
- Um servidor remoto confiável é pré-requisito: ele recebe o conteúdo somente
  quando a ferramenta é chamada.
