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

### Página web do Companion

`agentclip companion open m2` abre uma página local protegida por uma URL com
token de visualização. Ela não é publicada na rede: o servidor HTTP escuta em
`127.0.0.1`. Não compartilhe essa URL.

A página atualiza a cada dois segundos e mostra:

- perfil e destino SSH;
- estado do túnel (`Conectado` ou `Desconectado`) e o último erro, quando há;
- clipboard armado, quantidade, nomes/tipos dos itens e horário de expiração;
- botão **Parar Companion**, equivalente ao comando `companion stop`.

Ela é uma visão operacional; não transfere o conteúdo do clipboard ao
servidor. A transferência acontece apenas quando o harness remoto chama uma
ferramenta MCP após um pedido explícito do usuário.

Quando um agente remoto oferece um arquivo para o host, esta página mostra a
seção **Arquivos do servidor**. Ela exibe nome, tamanho e expiração da oferta;
use **Aceitar** para liberar a entrega ou **Recusar** para cancelá-la. Ao
aceitar ou recusar, a oferta sai imediatamente da lista e a chamada MCP remota
que a criou recebe a decisão (ela espera por até 10 minutos). O remoto não
consegue enviar bytes nem escolher o destino local antes desse aceite. Um
recebimento validado fica em `~/.cache/agentclip/received/` (ou cache
equivalente) e é removido automaticamente após 30 minutos.

Arquivos recebidos de texto, código e dados tabulares exibem também **Abrir
conteúdo**. O botão abre uma nova aba local com o conteúdo bruto como texto
simples — apropriado para CSV, TSV, SQL, JSON, YAML e fontes. Ele não executa
HTML e não é exibido para formatos binários, como imagens e planilhas `.xlsx`.
Todos os arquivos recebidos exibem ainda **Baixar**, que baixa o original pelo
endereço privado local, e **Copiar caminho**, para copiar sua localização na
inbox do AgentClip.

### `doctor`

```text
agentclip doctor
```

Verifica se há um bridge local saudável e informa endereço e PID. Para o fluxo
com Companion, prefira `agentclip companion status <perfil>`, que também inclui
o estado do túnel e do clipboard.

### `version`

```text
agentclip version
agentclip --version
agentclip -v
```

Exibe a versão do binário. Os instaladores usam esse comando para decidir se
uma atualização é necessária.

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

Os perfis persistem, mas AgentClip não registra autostart no sistema. Depois de
reiniciar o host, inicie explicitamente o perfil desejado:

```bash
agentclip companion start m2
```

Se apenas o servidor remoto reiniciar, um Companion que permaneça vivo no host
tentará restabelecer automaticamente o mesmo túnel, com espera progressiva de
1 a 30 segundos.
