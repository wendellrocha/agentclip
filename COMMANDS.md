# Referência de comandos

Esta é a referência da interface de linha de comando do AgentClip. Nos
exemplos, `m2` é o nome de um perfil local e `bastion-m2` é um destino aceito
pelo SSH (host, alias de `~/.ssh/config` ou `usuario@host`).

## Fluxo recomendado

```bash
agentclip setup bastion-m2 --profile m2
agentclip companion open m2
ssh bastion-m2
codex
```

`setup` cria o perfil, configura os harnesses suportados que já existirem no
servidor e inicia o Companion. Depois disso, use o SSH normalmente e peça ao
agente para examinar o clipboard.

## Comandos do usuário

### `setup`

```text
agentclip setup <ssh-destination> [--profile NAME]
  [--agent all|codex|claude|gemini|agy|opencode|pi]
  [--version vX.Y.Z] [--remote-port 39123]
  [--skip-agent] [--skip-install] [--no-start]
```

É a forma recomendada de configurar um servidor. Instala a release compatível
no servidor Linux/macOS via SSH, cria ou substitui o perfil local, detecta e
configura os harnesses já instalados e, por padrão, inicia o Companion local.

- `--profile`: nome local do pareamento. Sem ele, o nome é derivado do destino.
- `--agent`: limita a configuração a um harness; `all` é o padrão.
- `--version`: escolhe a versão de release a instalar remotamente.
- `--remote-port`: porta loopback do servidor reservada ao túnel reverso.
- `--skip-agent`: não registra a integração MCP no servidor.
- `--skip-install`: não instala ou atualiza o binário remoto.
- `--no-start`: conclui a configuração sem iniciar o Companion.

Antes das etapas remotas, `setup` testa uma autenticação SSH por chave sem
interação. Se ela funcionar, preserva a configuração existente. Se o destino
aceitar somente senha, gera uma chave Ed25519 privada do AgentClip, pede a
senha uma vez para adicioná-la ao `authorized_keys` remoto e usa essa chave nos
túneis seguintes. A chave local fica em `os.UserConfigDir()/agentclip/keys/`
com permissões privadas; `pair` não executa esse bootstrap.

Executar `setup` outra vez para o mesmo perfil gera um novo token de
pareamento e para o Companion anterior, caso esteja em execução.

### `pair`

```text
agentclip pair <profile> <ssh-destination>
  [--agent all|codex|claude|gemini|agy|opencode|pi]
  [--remote-port 39123] [--skip-agent]
```

Cria somente o pareamento local e configura o harness remoto. É útil para
desenvolvimento ou quando o `agentclip` já foi instalado manualmente no
servidor. Diferentemente de `setup`, não instala o binário remoto e não inicia
o Companion; faça isso com `agentclip companion start <profile>`.

### `connect`

```text
agentclip connect <profile>
  [--agent all|codex|claude|gemini|agy|opencode|pi]
```

Lê um perfil existente e (re)configura a integração AgentClip nos harnesses
remotos. Não reinicia o Companion nem altera o token ou a porta do perfil.

### `uninstall` e `disconnect`

```text
agentclip uninstall <profile> --agent <codex|claude|gemini|agy|opencode|pi>
agentclip disconnect <profile> --agent <codex|claude|gemini|agy|opencode|pi>
```

Remove somente a entrada MCP ou extensão do AgentClip do harness indicado no
servidor. `disconnect` é um alias de compatibilidade. Nenhum dos dois remove o
CLI do harness, o binário remoto, o perfil local ou o Companion em execução.

### `companion`

```text
agentclip companion <start|stop|status|open|view|run|inbox> <profile>
agentclip companion <accept|reject> <profile> <offer-id>
agentclip companion autostart <enable|disable|status> <profile>
```

Gerencia a parte local persistente do AgentClip.

- `start`: inicia o Companion em background. Ele acompanha alterações no
  clipboard, mantém o bridge local e abre o túnel SSH reverso.
- `stop`: pede uma parada limpa do Companion e encerra o túnel.
- `status`: imprime em JSON o perfil, o destino, o estado do túnel e os itens
  atualmente armados no clipboard.
- `open` ou `view`: abre a página web local do Companion no navegador padrão.
- `run`: executa o Companion em primeiro plano; use para diagnóstico. `start`
  é preferível no uso normal.
- `inbox`: mostra o estado do Companion, incluindo arquivos oferecidos pelo
  servidor e os recebimentos recentes.
- `accept` e `reject`: aprovam ou recusam uma oferta pendente sem abrir o
  navegador. O ID aparece em `inbox`, `status` ou na página web.
- `autostart enable`: faz o Companion do perfil subir quando você entra na
  sessão, com o que cada sistema já tem: um LaunchAgent em
  `~/Library/LaunchAgents` no macOS, uma unidade de usuário do systemd em
  `~/.config/systemd/user` no Linux e uma tarefa `ONLOGON` do Agendador de
  Tarefas no Windows. O serviço executa `agentclip companion serve <perfil>` e
  reinicia o processo se ele falhar. Ativar de novo recarrega a definição, e o
  perfil precisa existir. O serviço não abre um segundo Companion se já houver
  um rodando para o perfil.
- `autostart disable`: para o serviço e remove os arquivos que o `enable`
  criou. O Companion que o serviço iniciou é parado; um que você iniciou por
  conta própria continua rodando. Pode ser repetido sem erro, mas se o sistema
  se recusar a desligar o serviço o comando falha em vez de dizer que deu certo.
- `autostart status`: diz se o perfil sobe no login. No Linux e no Windows a
  resposta vem do próprio sistema (`systemctl is-enabled`, Agendador de
  Tarefas); no macOS, do LaunchAgent existir em `~/Library/LaunchAgents`, que
  é o que o faz carregar no login.

Um serviço de login não herda o seu terminal. Se o perfil está em um diretório
dado por `AGENTCLIP_CONFIG_DIR`, o `enable` grava essa variável na definição do
serviço (LaunchAgent e systemd); no Windows, onde a tarefa não carrega variáveis,
o `enable` recusa e explica. O diretório de logs do macOS é criado no `enable`,
porque o launchd abre os arquivos de log antes de iniciar o processo.

### `upgrade`

```text
agentclip upgrade
```

Baixa a release estável mais recente para a plataforma local, confere o
SHA-256 publicado em `checksums.txt` e o atestado de build do GitHub (a partir
da `v0.7.1-rc.1`; `AGENTCLIP_SKIP_ATTESTATION=1` ignora essa checagem) e
substitui o executável que está em uso.
Também atualiza todos os destinos dos perfis salvos, usando a identidade SSH
gerenciada pelo perfil quando houver. Para cada servidor, esta máquina baixa e
verifica o binário da plataforma dele (`uname`) e o envia por SSH; nenhum
instalador é baixado para o servidor, e uma versão igual ou mais nova que já
esteja lá não é substituída. Uma falha em um servidor não impede as
tentativas nos demais nem a atualização local; o resumo final identifica cada
perfil que falhou.

A ordem é: baixar e verificar o binário desta máquina (a etapa mais lenta, que a
saída anuncia), atualizar cada servidor, e só então parar os Companions em
execução, trocar o executável local e reiniciar esses Companions. A troca local
fica por último porque é a única parte difícil de desfazer: se algo falhar antes
dela, esta máquina fica como estava, e os túneis seguem no ar durante o trabalho
nos servidores.

Se esta máquina já está na release mais recente, nada é baixado, trocado ou
reiniciado aqui: o comando só atualiza os servidores que ainda estiverem
atrás, e o resumo distingue `updated` de `already up to date`.

Antes da troca local, o comando registra e para os Companions saudáveis. Após
a troca, reinicia somente esses perfis; um Companion que já estava parado
permanece parado. Em Windows, a troca é concluída por um pequeno processo
PowerShell depois que o comando termina, porque o `.exe` em execução fica
bloqueado pelo sistema.

### Página web do Companion

`agentclip companion open m2` abre uma página local protegida por uma URL com
token de visualização. Ela não é publicada na rede: o servidor HTTP escuta em
`127.0.0.1`. Não compartilhe essa URL.

A página aparece em inglês por padrão. Para vê-la em português, abra o endereço
com `?lang=pt-BR`, deixe o navegador preferir `pt-BR` ou inicie o Companion com
`AGENTCLIP_LANG=pt-BR` (veja [Idioma](#idioma)). Os nomes dos botões abaixo estão
como aparecem em português; o original em inglês vem entre parênteses na
primeira menção.

A página atualiza a cada dois segundos e mostra:

- perfil e destino SSH;
- estado do túnel (`Conectado` ou `Desconectado`; em inglês, `Connected` ou
  `Disconnected`) e o último erro, quando há;
- clipboard armado, quantidade, nomes/tipos dos itens e horário de expiração;
- aviso de release estável nova, com a versão e `agentclip upgrade`;
- botão **Parar Companion** (*Stop Companion*), equivalente ao comando `companion stop`.

Ela é uma visão operacional; não transfere o conteúdo do clipboard ao
servidor. A transferência acontece apenas quando o harness remoto chama uma
ferramenta MCP após um pedido explícito do usuário.

Quando um agente remoto oferece um arquivo para o host, esta página mostra a
seção **Arquivos recebidos** (*Received files*). As ofertas pendentes e os arquivos já recebidos
são exibidos separadamente, cada grupo ordenado do mais recente para o menos
recente. A oferta mostra data e hora de recebimento, tamanho e expiração; use
**Aceitar** (*Accept*) para liberar a entrega ou **Recusar** (*Reject*) para cancelá-la. Ao
aceitar ou recusar, a oferta sai imediatamente da lista e a chamada MCP remota
que a criou recebe a decisão (ela espera por até 10 minutos). O remoto não
consegue enviar bytes nem escolher o destino local antes desse aceite. Um
recebimento validado fica em `~/.cache/agentclip/received/` (ou cache
equivalente) e é removido automaticamente após 30 minutos. Os recebidos exibem
também a data e hora em que a transferência foi concluída.

Arquivos recebidos de texto, código e dados tabulares exibem **Abrir conteúdo** (*Open content*)
e **Copiar conteúdo** (*Copy content*). Abrir conteúdo mostra o texto bruto numa nova aba local;
Copiar conteúdo copia esse texto para a área de transferência. As ações servem
para CSV, TSV, SQL, JSON, YAML e fontes, não executam HTML e não são exibidas
para formatos binários, como imagens e planilhas `.xlsx`. Todos os arquivos
recebidos exibem ainda **Baixar** (*Download*), que baixa o original pelo endereço privado
local, e **Copiar caminho** (*Copy path*), para copiar sua localização na inbox do AgentClip.

### `doctor`

```text
agentclip doctor
```

Verifica se há um bridge local saudável e informa endereço e PID. Para o fluxo
com Companion, prefira `agentclip companion status <perfil>`, que também inclui
o estado do túnel e do clipboard.

### `logs`

```text
agentclip logs <profile>
agentclip logs <profile> --export caminho/agentclip.log
```

Mostra o caminho do log privado do Companion ou exporta uma cópia completa
para o caminho informado. Para coletar detalhes adicionais durante a
reprodução de um problema, reinicie o perfil com:

```text
agentclip companion stop <profile>
agentclip companion start <profile> --verbose
```

O modo verbose registra eventos de clipboard e do túnel SSH, além de mensagens
de erro. Ele não registra tokens, bytes nem o conteúdo do clipboard.

O Companion consulta a release estável no início e no máximo uma vez a cada 24
horas, compartilhando um cache privado entre perfis. O status também está na
ferramenta MCP `agentclip_update_status`; ela nunca exige que o clipboard esteja
armado e recomenda `agentclip upgrade` quando necessário.

### `help`

```text
agentclip help
agentclip help <command>
agentclip <command> --help
```

Sem argumentos, `agentclip` (ou `help`, `-h`, `--help`) lista todos os comandos
agrupados, cada um com uma linha de resumo. `agentclip help <comando>` (ou
`agentclip <comando> --help`) mostra a sintaxe e explica o comando e suas
opções, incluindo os aliases. Pedir ajuda nunca executa o comando. Um comando
desconhecido diz isso, aponta para `help` e sai com o código 2.

### `version`

```text
agentclip version
agentclip --version
agentclip -v
```

Exibe a versão do binário. Os instaladores usam esse comando para decidir se
uma atualização é necessária.

## Idioma

A saída da linha de comando, o `help`, as mensagens dos instaladores e a página
web do Companion estão em inglês (en-US) por padrão, e existe um catálogo em
português do Brasil (pt-BR). A escolha:

- **CLI e instaladores** (`install.sh`, `install.ps1`): a variável
  `AGENTCLIP_LANG`, por exemplo `AGENTCLIP_LANG=pt-BR agentclip help`. Aceita
  `pt`, `pt-BR`, `pt_BR.UTF-8` e variantes; qualquer outro valor, ou nenhum, é
  inglês. O idioma do sistema **não** é consultado, então o mesmo comando imprime
  as mesmas palavras em qualquer máquina, a menos que você peça outro idioma.
- **Página do Companion**: `?lang=pt-BR` no endereço, depois o idioma que o
  navegador prefere (`Accept-Language`), depois `AGENTCLIP_LANG` do processo do
  Companion e, por fim, inglês.

Traduzem-se o que a pessoa lê: o `help`, as mensagens de resultado e de estado, os
avisos e a página. As mensagens de erro técnicas ficam em inglês de propósito, para
poderem ser pesquisadas. O que faltar em um catálogo aparece em inglês.

## Fluxo avulso legado

### `arm`

```text
agentclip arm
```

Captura uma imagem do clipboard e a arma no bridge local por 90 segundos. É
destinado ao fluxo avulso com `agentclip ssh`; o Companion é o caminho moderno
para imagens, texto e arquivos.

### `ssh`

```text
agentclip ssh <ssh-destination> [-- <ssh arguments>]
```

Abre uma sessão SSH temporária para a imagem previamente armada por `arm` e
cria um encaminhamento reverso somente durante essa sessão. Os argumentos após
`--` são repassados ao SSH. Não substitui `setup` + Companion para o uso
cotidiano.

## Comandos de integração e internos

### `harness`

```text
agentclip harness <install|remove> <agy|opencode|pi> --name NAME
  [--port PORT --token TOKEN]
```

Instala ou remove diretamente a configuração global de AGY, OpenCode ou Pi no
usuário atual. `install` requer `--port` e `--token`. Normalmente este comando
é chamado no servidor por `setup`, `pair`, `connect` e `uninstall`; use-o
manualmente apenas para depurar uma integração.

### `mcp`

```text
agentclip mcp
```

Inicia o servidor MCP via `stdio`. Requer `AGENTCLIP_BRIDGE_PORT` e
`AGENTCLIP_SESSION_TOKEN`, que são configurados automaticamente pelos
adaptadores. Não é necessário executá-lo diretamente em um terminal.

### `bridge`

```text
agentclip bridge
```

Processo interno que recebe a inicialização pelo `stdin` e mantém o bridge HTTP
local. É iniciado por `arm` ou pelo Companion; não deve ser executado
manualmente.

## Após reinicializações

Os perfis persistem, mas por padrão o AgentClip não registra autostart no
sistema. Depois de reiniciar o host, inicie explicitamente o perfil desejado:

```bash
agentclip companion start m2
```

Para dispensar isso, ative `agentclip companion autostart enable m2`.

Se apenas o servidor remoto reiniciar, um Companion que permaneça vivo no host
tentará restabelecer automaticamente o mesmo túnel, com espera progressiva de
1 a 30 segundos.
