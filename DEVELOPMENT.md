# Desenvolvimento

## Pré-requisitos

- Go 1.27 ou superior;
- SSH para um servidor de teste, caso queira validar o fluxo completo;
- Ao menos um harness suportado instalado no servidor: Codex, Claude Code ou
  Gemini CLI.

## Build e testes

```bash
go build -o ./agentclip ./cmd/agentclip
go test -race ./...
go vet ./...
```

O binário local `./agentclip` e a pasta `plans/` são ignorados pelo Git.

A versão de desenvolvimento vive em `internal/buildinfo/buildinfo.go`. As
releases usam a tag `vX.Y.Z` para sobrescrevê-la no binário e nos metadados MCP;
para iniciar o próximo ciclo, altere apenas esse arquivo.

## Fluxo de desenvolvimento

Para uma instalação remota manual durante o desenvolvimento:

```bash
go build -o ./agentclip ./cmd/agentclip
scp ./agentclip bastion-m2:~/.local/bin/agentclip
ssh bastion-m2 'agentclip version'

./agentclip pair m2 bastion-m2 --remote-port 39123
./agentclip companion start m2
./agentclip companion open m2
```

`pair` mantém esse fluxo avançado. `setup` é o caminho recomendado para uma
release publicada; ele não tenta baixar uma versão de desenvolvimento sem que
`--version vX.Y.Z` seja informado.

`pair`, `setup` e `connect` detectam e configuram todos os harnesses suportados
instalados no servidor. Para limitar a um harness ou remover exclusivamente a
entrada MCP do AgentClip, use:

```bash
./agentclip connect m2 --agent claude
./agentclip connect m2 --agent gemini
./agentclip uninstall m2 --agent gemini
```

`disconnect` é mantido como alias compatível de `uninstall`. Nenhum dos dois
remove o binário ou desinstala o harness remoto.

## Validar arquivos e CSV

1. Copie um arquivo, como `vendas.csv`, no Finder, Explorer ou gerenciador de
   arquivos compatível.
2. Confirme na view do Companion que o item foi armado:

   ```bash
   ./agentclip companion status m2
   ```

3. Abra uma sessão no servidor, inicie o Codex e peça: `Analise o arquivo CSV
   que está no meu clipboard.`
4. O agente deve chamar `clipboard_status` e
   `materialize_clipboard_files`. O arquivo aparecerá em
   `~/.cache/agentclip/inbox/` no servidor.

## Releases

Uma tag semântica `vX.Y.Z` dispara os workflows de teste e release. A release
publica binários para Linux `amd64`/`arm64`, macOS `amd64`/`arm64` e Windows
`amd64`, além de checksums e instaladores.

```bash
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

As notas de release são geradas pelo GitHub a partir dos pull requests. Labels
como `feature`, `bug`, `security`, `documentation`, `dependencies` e
`breaking-change` determinam as categorias. Use `ignore-for-release` ou
`skip-changelog` para omitir um PR. Sem PRs entre a tag anterior e a atual, o
workflow publica os commits desse intervalo como notas de release.
