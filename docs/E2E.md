# Validação ponta a ponta

Canal de WhatsApp por QR Code (**não homologado**) para Mautic 7.

## Estado em 06/10/2026

O pareamento foi confirmado pelo operador no celular e pelo servidor às
02:59:48 UTC: `PairSuccess`, autenticação às 02:59:49 e `Connected` às 02:59:52.
Uma credencial está salva no SQLite e o Mautic registrou status, JID e telefone.
A página de pareamento mostra conectado. O erro de rota do botão de Inbox foi
corrigido e validado no navegador.

Antes da atualização, a tentativa permaneceu em pareamento e expirou sem
`PairSuccess`. O serviço foi atualizado de `2e338d0ee73d` para `6bb48c0f1ff0`,
com versão Web 2.3000.1049294120. O novo código conectou. Isso comprova o resultado
da atualização e nova tentativa, mas não isola qual mudança resolveu a falha.
Não há evidência atual para atribuir a falha anterior à reativação do chip.

| Prova | Resultado |
|---|---|
| Pareamento real no celular | Confirmado pelo operador |
| Autenticação e sessão persistida | Confirmadas no servidor |
| Estado e telefone no Mautic | Confirmados por consulta somente leitura |
| Página mostra conectado e abre Inbox | Validada no navegador |
| Mensagem real recebida no Inbox | Confirmada na conversa de teste |
| Resposta real pelo Inbox | Confirmada pelo destinatário às 03:24 UTC |
| Reinício sem novo QR | Confirmado: restauração e autenticação às 03:07:15 UTC |
| Queda prolongada e fila em ordem | Ainda não validadas nesta instalação |
| Recibos de leitura | Não implementados no plugin |

Verificações locais: 76 testes PHP do plugin (350 asserções), testes unitários
Go de API/webhook e 34 funções sem banco de sessão/estado, 346 arquivos PHP sem
erros de sintaxe. Nenhum teste de banco foi executado contra produção.

Backups verificados ficam em `/home/forge/whatsqr-backups/20261006T024148Z` e
`/home/forge/whatsqr-backups/20261006T025626Z`; incluem dump Mautic e cópia SQLite
consistente. Contêm segredos e não devem ser publicados.

## Prova histórica de opt-out

Este registro descreve uma validação anterior em banco descartável. Ela não foi executada novamente nesta publicação; os testes desta release usam mocks e não acessam o banco do Mautic.

`MauticInboxBundle/Tests/Functional/QrChannelRespectsOptOutTest.php`, na release
de provas contra o banco descartável. Roda sem sessão pareada porque a recusa
acontece **antes** de o remetente escolher o transporte.

É a garantia que impede o canal não homologado de ser o único que ignora quem
pediu para sair. A checagem de consentimento mora no remetente do conector, não
na borda; um dos desenhos considerados dava ao plugin o próprio remetente, e
teria levado exatamente a isso.

Conferido pelos dois lados: desligar a checagem só para asset QR deixa o teste
vermelho.

**A mensagem da exceção faz parte da afirmação.** Sem ela o teste passava mesmo
com o canal pulando o consentimento inteiro, porque alguma coisa mais adiante
— sessão não configurada, transporte ausente — também estoura `DomainException`.

### O que este E2E encontrou de quebrado

A primeira versão passava **pela exceção errada**: a janela de 24 horas do WABA
estava sendo cobrada do canal por QR e disparava antes do opt-out.

Isso fechava o canal nas duas situações que são o uso normal dele — começar uma
conversa, e responder depois de um dia parado — e a saída que a mensagem
oferecia, "use um template aprovado", **não existe neste canal**. O atendente
leria uma instrução impossível de seguir e a conexão pareceria quebrada.

Corrigido no conector (`OutboundPolicy`), com os limites de anti-spam mantidos
de propósito: um número não homologado é justamente onde disparar em massa
termina em banimento.

## Próximas provas controladas

1. Abrir a sessão pela tela de Conexões, não por `curl` — é a tela que precisa
   ser provada.
2. E2E 4 é o mais importante, e **anotar o tempo real** faz parte dele. Use
   `bin/medir-fila.sh <id-da-conversa>`: ele imprime uma linha a cada cinco
   segundos com quantas respostas estão pendentes, em retry e enviadas. Está
   escrito de antemão de propósito — ninguém escreve isso com o celular na mão e
   o cronômetro correndo. A ordem
   por conversa foi resolvida dentro do `findDue()`, e isso tem um custo
   conhecido: duas mensagens vencidas da mesma conversa não saem no mesmo lote.
   Com varredura de minuto em minuto, três respostas represadas gotejam por três
   minutos. Com três pode ser irrelevante; com quinze não é. **Medir antes de
   decidir se vale mexer.**
3. Escrever aqui o resultado de cada um, com data e número usado.

## SSE e resposta pelo Inbox — 6 de outubro de 2026

- `EventSource` autenticado em `/s/whatsqr/connections/16/pair/events`: indicador
  Live, cartão Connected e nenhum erro no console. Navegação para a lista e retorno
  encerram/reabrem o fluxo automaticamente.
- SSE interno: 401 sem bearer; 200 `text/event-stream` com autenticação, retrato
  da sessão `suporte` conectada e nenhum QR após o pareamento.
- Nginx sem buffering e pool PHP 8.4 separado, com 4 workers sob demanda; sessão
  PHP liberada antes do stream. Não há consultas de estado ao banco em loop.
- Backup consistente do SQLite pareado com verificação de integridade e hashes:
  `/home/forge/whatsqr-backups/sse-20261006T032059Z`. Sem alterações de schema.
- Checagens locais sem banco: 81 testes PHP / 371 assertions, 4 testes JavaScript,
  testes Go da API/webhook com race detector e seleção de testes puros de sessão
  (estado, concorrência, QR, assinatura e resolução do destinatário). Testes Go
  do store/SQLite não foram executados.
- A resposta do operador na conversa Inbox 269 falhava porque o conector normalizava
  o número brasileiro para 9 dígitos, enquanto o WhatsApp usa o identificador antigo.
  O serviço agora consulta o identificador canônico antes do envio e confirma a
  variante com 8 dígitos se necessário. Não reescreve o cadastro do contato.
- O job existente 75 foi retentado pela fila normal, com claim atômico e somente
  para o destinatário autorizado. Resultado completed, log 365, com ID externo
  retornado pelo WhatsApp. O Inbox mudou de Waiting to retry para Sent sem reload.
  O recebimento foi confirmado pelo operador na própria conversa às 03:24 UTC
  ("sim, recebi lega"). Uma segunda resposta às 03:25 UTC também passou para Sent.

## Publicação 0.2.1 / Meta 0.14.2 / Inbox 1.4.1

- WhatsQR: 81 testes PHP (371 asserções), 4 testes JavaScript e testes puros Go de API/webhook/sessão com race detector.
- Meta: 63 testes com mocks e objetos em memória (266 asserções). Os 3 testes de repositório SQLite são bloqueados por padrão e exigem autorização explícita de um arquivo descartável, prova da conexão e backup antes do schema.
- Inbox: 4 testes de disponibilidade QR (9 asserções), 80 testes JavaScript/TypeScript, formatação, Svelte check sem avisos e build dos bundles.
- A publicação preserva o main mais recente de cada dependência. Não substitui a instalação de produção por um snapshot antigo e não executa migrações.

## Fotos de perfil no Inbox — 6 de outubro de 2026

- A conversa de teste mostrou a foto real no item da lista, no cabeçalho e no painel do contato. As três imagens carregaram pela rota autenticada do Mautic; nenhum erro apareceu no console do navegador.
- O serviço privado respondeu 401 sem autenticação e 200 com uma imagem JPEG válida. A primeira consulta levou aproximadamente 0,436 segundo; uma nova consulta veio do cache em 0,001 segundo.
- O cache é separado por sessão e destinatário, limitado a 8 MiB e 256 entradas. Há no máximo duas buscas simultâneas no WhatsApp. Ausência de foto ou restrição de privacidade mantém as iniciais.
- A extensão do Inbox apenas produz a URL local, sem consultas de banco ou chamadas remotas dentro do loop da lista. A rota de imagem faz uma consulta com o asset associado e libera a sessão PHP antes de buscar a foto.
- Testes sem banco: 87 testes PHP do WhatsQR / 399 asserções; 5 testes PHP de contrato/disponibilidade do Inbox / 11 asserções; 2 testes do componente compilado de Inbox, incluindo o fallback quando a imagem falha. API, webhook e testes puros de sessão Go passaram com race detector.
- O serviço restaurou o pareamento após o reinício. Backup consistente do SQLite, com integridade verificada, em `/home/forge/whatsqr-backups/avatar-20261006T034544Z`. Sem migrações ou testes de banco em produção.
- Esta validação cobre a foto de perfil atual. Recuperação de anexos de mensagens e sincronização de histórico não fazem parte desta entrega.

## Implementação de anexos recebidos — 6 de outubro de 2026

- O serviço captura imagem, vídeo, áudio, documento e figurinha com legenda/nome, mantendo as referências criptográficas somente em disco privado. Visualização única não é arquivada.
- Testes locais sem banco: WhatsQR 93 testes PHP / 435 asserções; contrato/apresentação do Inbox 3 testes / 14 asserções. O teste do bundle Svelte compilado confirmou imagem, vídeo com controles, áudio e link de documento usando a rota QR protegida, além das funções existentes de conversa/envio/rascunho.
- Go: API, webhook, armazenamento de arquivos e testes puros selecionados de sessão passaram com race detector; `go vet` passou. Cobertura inclui autenticação, isolamento por sessão, byte ranges/ETag, cache após reinício, coalescência, concorrência máxima, limite de escrita, MIME incorreto, tamanho e symlinks. Sem testes de store/SQLite ou kernel Mautic.
- Deploy: 12 arquivos PHP passaram na sintaxe local/produção; cache prod recompilado; serviço reiniciado conectado com credencial preservada. Rota Mautic registrada; rota interna respondeu 401 sem bearer e 404 para arquivo inexistente autenticado. Diretório de mídia 0700. Backup consistente e verificado: `/home/forge/whatsqr-backups/media-20261006T043452Z`.
- O binário instalado tem SHA256 `0bd0cee65e3fe168357c373426f7cfe45f87d6aa1a6f9b3f27d70e7fb91a27e8`.
- Esta etapa de implantação foi seguida pela prova real de imagem/PDF descrita abaixo, na mesma conversa de teste.
- Anexos anteriormente descartados precisam ser reenviados. Esta entrega não sincroniza histórico antigo nem envia anexos pelo compositor.

## Validação estrita de anexos — 6 de outubro de 2026

- PHP: 94 testes puros do WhatsQR / 454 asserções; contrato de mídia do Inbox: 3 testes / 14 asserções; componente Svelte compilado: 2 testes aprovados. Nenhum kernel ou banco de produção foi usado nos testes.
- Go: testes de media/API/webhook e testes selecionados de sessão com race detector, além de `go vet`, passaram. Casos adversariais cobrem MIME/extensão falsos, imagem truncada, pixels excessivos, animações GIF/WebP, scripts/HTML/SVG/executáveis, PDF ativo/criptografado, macros/OOXML externo, ZIP bomb/traversal, scanner ausente/infectado, corrupção de cache, symlinks, quota incluindo arquivos órfãos e ranges contraditórios.
- `govulncheck` identificou três vulnerabilidades alcançáveis na versão anterior de `golang.org/x/image`. Após a atualização para 0.45.0 e build com Go 1.26.8, a análise dos pacotes media/API/session não encontrou vulnerabilidades alcançáveis. Isso não equivale a declarar todos os módulos indiretos livres de vulnerabilidades.
- ClamAV 1.5.4 instalado no servidor com assinaturas oficiais e `freshclam` ativo/habilitado. Socket local `660 clamav:forge`, sem TCP público. Limites configurados de arquivo/scan/concurrency/timeouts e bloqueio de macros, criptografia e limites excedidos. Daemon limitado pelo systemd a 1 core de CPU e 1.8 GiB de RAM; consumo observado em torno de 965 MiB para assinaturas.
- Teste real do scanner, antes e depois do deploy, somente em filesystem temporário: PNG/PDF de teste liberados; assinatura inofensiva EICAR e HTML disfarçado recusados. Nenhuma mensagem de teste foi enviada a terceiros nem qualquer banco foi aberto pelo teste.
- Backup consistente do SQLite e arquivos privados/runtime verificado em `/home/forge/whatsqr-backups/security-20261006T105145Z`. Sem migrações. Somente 3 arquivos PHP atualizados; serviço Go substituído com SHA-256 `2aad875161862836ce1584cfa899bc2b8efac536379d94b780d7c292be8866ff`; sintaxe PHP 8.4 validada e FPM recarregado.
- A sessão WhatsApp foi restaurada e autenticada sem novo QR. A conversa 269 continuou visível no navegador, com fotos de perfil carregadas e sem erros no console. As rotas privadas de mídia retornaram 401 sem bearer e 404 para referência inexistente autenticada; pasta privada 0700.
- A prova real WhatsApp → Whatsmeow → Inbox foi concluída depois desta implantação e está descrita abaixo. O teste isolado do antivírus complementa a prova de entrega.

## Imagem e PDF reais — publicação 0.3.0 / Inbox 1.5.0

- Mensagens reais recebidas via WhatsApp Web e Whatsmeow na conversa de teste: texto às 13:54:29 UTC, imagem às 13:56:06 UTC e PDF às 13:59:00 UTC em 6 de outubro de 2026. Conteúdo sintético, sem dados pessoais.
- O Inbox recebeu os eventos sem recarregar a página. O preview carregou JPEG válido de 960 × 540 pixels, 40.962 bytes, convertido pelo WhatsApp a partir do PNG de entrada. O download autenticado também funcionou.
- PDF com 1.810 bytes baixado pela rota protegida: idêntico byte a byte ao original. SHA-256 `f1a733cdbfabf2807894591c5239f0f557edbb2529ee55962c8835cbb2fbd9c7`.
- Os dois arquivos em cache privado têm SHA-256 igual ao declarado pelo WhatsApp, modo 0600 e diretório 0700. Serviço QR e ClamAV ativos.
- Acesso sem autenticação aos dois endpoints Mautic retornou HTTP 403, sem conteúdo de mídia.
- `TestLiveClamdSecurity` passou novamente (0,04 s), incluindo HTML inofensivo disfarçado de PDF e EICAR recusados. Esse teste abre somente arquivos temporários: não conecta ao WhatsApp nem abre banco.
- Vídeo/áudio/figurinhas e bordas de tamanho têm cobertura automatizada; o teste real desta publicação exercitou somente imagem e PDF. Não há importação retroativa de arquivos antigos.
- Screenshots e dados de contatos usados na validação permanecem privados e não fazem parte dos assets públicos da release.
