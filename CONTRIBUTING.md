# Contribuindo para o AgentClip

Obrigado pelo interesse em contribuir.

## Antes de abrir um pull request

1. Discuta mudanças de escopo, protocolo ou segurança em uma issue primeiro.
2. Mantenha a alteração focada e inclua testes para o comportamento novo ou
   corrigido.
3. Execute localmente:

   ```bash
   go test -race ./...
   go vet ./...
   ```

4. Atualize a documentação quando o comportamento, comandos ou instalação
   mudarem.

## Pull requests

Explique o problema resolvido, a abordagem e como validou a alteração. Não
inclua segredos, tokens de sessão, conteúdo de clipboard ou arquivos obtidos de
servidores remotos nos commits ou nas descrições.

Use labels para que as notas de release sejam organizadas corretamente:

- `feature` ou `enhancement` para funcionalidades;
- `bug` para correções;
- `security` para mudanças de segurança;
- `documentation` para documentação;
- `dependencies` para dependências;
- `breaking-change` para mudanças incompatíveis;
- `ignore-for-release` ou `skip-changelog` quando o PR não deve aparecer.

## Áreas sensíveis

AgentClip move conteúdo privado entre máquina local e servidor remoto. Mudanças
em autenticação, permissões de arquivos, túnel SSH, paths, limpeza de arquivos
temporários ou comportamento automático devem preservar o princípio de acesso
explícito ao clipboard e ser revisadas com atenção extra.
