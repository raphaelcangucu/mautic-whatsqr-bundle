# Mautic WhatsApp QR

Plugin de conexão de números WhatsApp por QR para o Inbox multicanal do Mautic.
O serviço Go mantém a sessão com Whatsmeow; o plugin autentica os webhooks e usa
as conversas, contatos, consentimentos e fila do conector Meta.

Versão publicada: **0.3.0**, incluindo o plugin PHP e o serviço Go.

## Dependências

- Mautic 7, PHP 8.2 ou superior e [MauticMetaBundle 0.14.2](https://github.com/raphaelcangucu/mautic-meta-bundle/releases/tag/v0.14.2) ou compatível.
- [MauticInboxBundle 1.5.0](https://github.com/raphaelcangucu/mautic-inbox-bundle/releases/tag/v1.5.0) ou compatível, para atendimento.
- ClamAV com daemon `clamd`, socket Unix privado e assinaturas atualizadas por `freshclam` para liberar anexos.
- Go 1.26 para compilar, com preferência pelo patch 1.26.8 definido em `service/go.mod`; binário estático Linux/amd64 em produção.
- Whatsmeow fixado em `v0.0.0-20261005195255-6bb48c0f1ff0`.
- SQLite privado para as credenciais do dispositivo; banco do Mautic para contatos
  e conversas. Os dois são dados de produção.

## Funcionamento

1. Criar uma conexão interna Meta e um asset `whatsapp_qr_session`. O ID externo do
   asset deve coincidir com a chave da sessão no serviço, por exemplo `suporte`.
2. Configurar o endereço local, token e segredo de webhook no asset, criptografados
   pelo CredentialVault do conector. Nenhum token entra na página ou no QR.
3. Abrir `/s/whatsqr/connections`, selecionar o número e clicar em conectar.
4. Ler o código no WhatsApp em Aparelhos conectados. O painel acompanha a renovação
   e o resultado sem recarregar a página inteira.
5. Mensagens privadas recebidas entram no Inbox como WhatsApp · QR. Respostas de
   texto usam o transporte Whatsmeow através da fila existente do conector.

A navegação GET não abre nem apaga sessões. Iniciar e renovar usam POST, permissão
de edição e CSRF. Renovar não apaga credenciais de contas conectadas ou em
reconexão. Número e JID só são registrados após o evento real de pareamento.

## Gerenciar conexões pelo painel

Em `/s/whatsqr/connections`, **Editar nome** muda somente o nome interno da conta;
o ID da sessão, os segredos, o telefone, as conversas e o pareamento são preservados.
**Nova conexão** solicita um nome e o servidor de uma conexão QR já configurada.
A nova conta recebe um identificador aleatório e um segredo de webhook exclusivo,
criptografado pelo CredentialVault. Ela não herda o número nem a sessão do celular anterior.

Criar o cadastro redireciona para o pareamento. Somente **Gerar QR**, via POST com
CSRF e permissão de edição, registra a nova sessão no serviço e inicia o pareamento.
O serviço deve incluir `POST /sessions/{id}/configuration`: o registro é autenticado,
limitado e aditivo; nunca substitui segredos existentes. Registros adicionais ficam
em `<store_path>.sessions.json`, privado (0600), com gravação atômica. O serviço já
pareado não precisa ser reiniciado para adicionar outra conta. Atualize plugin e
serviço juntos ao habilitar esse fluxo pela primeira vez.

As ações exigem `meta:connections:create` / `meta:connections:edit`; o cadastro
continua usando `MetaAsset`, sem novas tabelas ou migrações. Formulários validam
CSRF, nome (1–191 caracteres) e servidor disponível. Sem servidor QR configurado,
o formulário informa a configuração necessária e não permite salvar.

## Serviço

Compilar em `service/`:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o whatsqr .
```

O binário Linux/amd64 e seu checksum estão disponíveis nos assets da [release v0.3.0](https://github.com/raphaelcangucu/mautic-whatsqr-bundle/releases/tag/v0.3.0). Verifique `SHA256SUMS` antes de substituir o serviço.

Executar com `whatsqr -config /caminho/privado/whatsqr.json`, como serviço systemd.
A configuração contém `listen`, `token`, `webhook_url`, `store_path`, `log_level`
e `sessions`, um mapa com `webhook_secret` por sessão. O processo recusa escutar
fora do loopback. Configuração e SQLite devem ter permissão 0600.

O webhook público é `/whatsqr/webhook`. Ele exige `X-WhatsQr-Key`, timestamp e
`X-WhatsQr-Signature`, um HMAC SHA-256 de timestamp seguido do corpo original.
A sessão assinada deve coincidir com a sessão do evento. Eventos são deduplicados
por conta e ID de mensagem.

API local autenticada: `GET /health`, `POST /sessions`, `GET /sessions/{id}/qr`,
`GET /sessions/{id}/events` (SSE), `POST /sessions/{id}/messages` e `DELETE /sessions/{id}`. DELETE apaga credenciais;
não deve ser usado para resolver indiscriminadamente problemas de rede.

A sessão salva é restaurada no reinício. Não use PM2 nem abra a porta 8088 na
Internet. Preserve o serviço de SSE do Inbox existente.

## Fotos de perfil no Inbox

O serviço consulta `GetProfilePictureInfo` no Whatsmeow e baixa a miniatura somente quando a foto é exibida. O navegador recebe `/s/whatsqr/avatars/{conversationId}`, protegido pelas mesmas permissões de conversas/mensagens do Inbox. Nenhum token, JID privado ou URL temporária do WhatsApp é enviado ao navegador.

A lista monta as URLs a partir das conversas já carregadas; não consulta o WhatsApp nem o banco em cada linha. A rota da imagem carrega conversa e asset em uma consulta e libera a sessão PHP antes da busca. O serviço mantém cache de uma hora para fotos e 15 minutos para fotos ausentes/restritas, coalescendo pedidos repetidos. Há no máximo duas buscas remotas simultâneas e 8 MiB / 256 entradas de cache.

Downloads aceitam somente miniaturas JPEG/PNG/WebP de até 256 KiB, vindas de HTTPS em CDNs reconhecidas, sem redirecionamentos. Foto não definida, restrição de privacidade ou sessão indisponível mantém as iniciais existentes. Conversas antigas também podem obter a foto atual; isso não recupera anexos de mensagens antigas.

A extensão opcional `ParticipantAvatarProviderInterface` está disponível no Inbox 1.5.0, assim como o contrato de anexos. Sem ela, o plugin QR continua funcionando e a busca de fotos não altera os demais canais.

## Imagens e anexos recebidos

Imagens, vídeos, áudios, documentos e figurinhas recebidos passam pelo mesmo fluxo de conversa, deduplicação e SSE do Inbox. O plugin preserva legenda e nome do arquivo. O contrato opcional `AttachmentProviderInterface`, com tag `mautic.inbox.attachment`, produz a URL autenticada `/s/whatsqr/media/{messageId}` sem chamadas remotas ou consultas dentro do loop de apresentação.

O serviço guarda somente a referência criptográfica em `${store_path}.media`, com diretórios 0700 e arquivos 0600, fora da raiz web. As chaves e o caminho temporário do WhatsApp não vão para o webhook, banco ou navegador. Ao exibir/abrir o anexo, `DownloadToFile` verifica os hashes e descriptografa diretamente em um arquivo limitado em disco. O conteúdo validado fica disponível após reinícios e durante quedas da sessão. A rota interna `GET /sessions/{id}/media/{mediaID}` exige bearer, isola sessões e atende byte ranges para vídeo/áudio; a rota do Mautic libera a sessão PHP e transmite sem juntar o arquivo em memória.

Limites: 32 MiB por anexo, inclusive durante o download criptografado; duas buscas/análises simultâneas, até 32 downloads em andamento; 2 GiB / 50 mil referências no armazenamento privado. Ao atingir o limite, novas gravações/downloads são recusados, sem apagar o histórico automaticamente. Monitore o diretório e inclua-o no backup privado junto ao SQLite. Não arquivamos mídia de visualização única.

### Segurança dos arquivos

O serviço exige ClamAV `clamd` no socket Unix `/run/clamav/clamd.ctl`. Nunca há fallback sem antivírus: resultado infectado, erro, timeout, resposta desconhecida ou scanner indisponível impedem a entrega. A varredura usa `INSTREAM`, sem shell ou caminho fornecido pelo usuário, com timeout de 20 segundos e apenas duas operações concorrentes. O hash SHA-256 esperado também é conferido antes de liberar bytes, inclusive nos arquivos em cache.

- MIME é identificado pelo conteúdo e comparado à extensão e ao MIME declarado. Um `application/octet-stream` declarado não autoriza conteúdo desconhecido.
- Previews admitem JPEG, PNG, GIF e WebP, vídeos MP4/WebM/3GP e os formatos de áudio explicitamente listados no código. Imagens têm limite de 16 milhões de pixels e 8.192 pixels por dimensão; GIF/WebP animados têm limites adicionais de quadros e pixels acumulados. Cabeçalhos falsos ou imagens truncadas são recusados.
- Documentos admitidos: PDF, TXT/CSV em UTF-8 e DOCX/XLSX/PPTX sem macros, objetos incorporados ou vínculos externos. OOXML é inspecionado como ZIP com limites de entradas, expansão e profundidade XML, sem extrair arquivos. PDFs têm checagens complementares de estrutura e objetos ativos. Isso não é CDR nem prova de ausência de toda ameaça.
- HTML, SVG, scripts, executáveis, arquivos compactados genéricos, arquivos criptografados detectados pelo antivírus e tipos desconhecidos são bloqueados. A validação de PDF não substitui o antivírus.
- Todo documento é entregue como download, mesmo PDF. As respostas usam `nosniff`, CSP restritiva e cache privado; nunca há uma URL pública direta para o diretório de arquivos. Nome do arquivo não define seu caminho de armazenamento.
- Destinos de mídia exigem HTTPS em domínios oficiais WhatsApp/Meta, sem redirecionamentos. Respostas com tamanho incompatível, múltiplos ranges ou MIME não permitido são recusadas.

A instalação do ClamAV deve manter `freshclam` ativo e um socket acessível somente ao usuário/grupo do serviço, por exemplo `LocalSocketGroup forge` / `LocalSocketMode 660`. Não habilite TCP público. Configure `StreamMaxLength 32M`, `MaxFileSize 32M`, `MaxScanSize 64M`, `MaxThreads 2`, `MaxQueue 8`, `MaxScanTime 15000`, `MaxFiles 1000`, `MaxRecursion 10`, `AlertExceedsMax true`, `AlertEncrypted true` e `OLE2BlockMacros true`. Com atualizações automáticas, habilite o reload local protegido e prefira `ConcurrentDatabaseReload false` em máquinas pequenas. Limite CPU/memória do daemon via systemd conforme a capacidade do host; o daemon mantém as assinaturas em memória e não deve ser executado uma vez por requisição PHP.

Validação isolada do antivírus (somente filesystem temporário, sem WhatsApp ou banco): compile `go test -c ./media` e execute `WHATSQR_TEST_CLAMD=1 ./media.test -test.run '^TestLiveClamdSecurity$' -test.v`. O teste usa a assinatura inofensiva EICAR somente em memória. Os testes usuais substituem o scanner por um mock; produção usa o ClamAV obrigatório.

Anexos recebidos antes dessa implementação não possuem as referências necessárias: precisam ser reenviados. Referências mantidas podem deixar de ser baixáveis se o WhatsApp expirar o arquivo antes do primeiro download. Não há sincronização/importação retroativa do histórico nem envio de anexos pelo compositor QR nesta entrega.

## Limites desta primeira versão

- Recepção de mensagens privadas e envio de texto. Grupos, status e newsletters
  não são convertidos em atendimentos privados.
- Mídia privada recebida pode ser exibida/baixada; envio de anexos neste transporte ainda não está implementado. Tipos desconhecidos continuam visíveis como conteúdo não suportado.
- Templates e janela da Cloud API não se aplicam ao canal QR. Consentimento,
  opt-out e limites de envio continuam no conector.
- O serviço captura recibos, mas o plugin ainda não aplica esses recibos às
  mensagens de saída no Mautic. Não considerar confirmação de leitura validada.
- A fila de webhooks do serviço é limitada e em memória; após falhas prolongadas
  ou reinício, eventos pendentes podem ser perdidos. Não anunciar entrega garantida.

## Operação e validação

Use INFO para diagnosticar conexão. Os novos logs registram versão de cliente,
renovação do QR e tipos de eventos, sem QR, chaves ou corpo de mensagens.
DEBUG da biblioteca pode registrar dados sensíveis e não deve ser ativado em
produção indiscriminadamente.

Faça backup consistente do SQLite, incluindo WAL, antes de atualizar uma versão
que altera o esquema. Faça backup do Mautic antes de alterações de esquema.
Não execute testes de banco na instalação ao vivo. Prefira testes unitários com
mocks, sintaxe e validação no navegador; testes de SQLite devem usar somente
arquivos descartáveis explicitamente autorizados pela guarda de testes.

Resultados reais e pendências: [docs/E2E.md](docs/E2E.md).

## Alternativa investigada

[WAHA](https://waha.devlike.pro/docs/how-to/engines/) oferece uma API REST e
webhooks sobre vários motores. WEBJS usa WhatsApp Web em Chromium/Puppeteer e
é uma alternativa com implementação diferente. GOWS usa Whatsmeow e não é um
fallback independente para falhas do mesmo protocolo. WEBJS exige um processo
de navegador: sua memória e CPU precisam ser medidas antes de instalar no
servidor compartilhado. WAHA foi pesquisado, não instalado nem validado com
essa conta. O pareamento real já funciona no serviço Whatsmeow atualizado.

## Pareamento em tempo real (SSE)

A página de pareamento usa `EventSource` em `/s/whatsqr/connections/{assetId}/pair/events`.
Essa rota exige login e a permissão `meta:connections:view`. O PHP libera a sessão
antes de acompanhar o SSE privado do serviço Go; o token permanece no servidor.

O serviço publica um retrato inicial e mudanças de QR/estado em memória, por conta,
sem consultas periódicas ao banco ou requisições `/health` em loop. Assinantes lentos
recebem o estado mais recente, sem bloquear o WhatsApp. Heartbeats mantêm o fluxo;
as conexões duram no máximo 25 segundos e são renovadas automaticamente com cursor.
O navegador conserva o cartão se o estado não mudou, usa retentativa com espera
crescente após falhas e encerra o fluxo em páginas ocultas ou ao navegar.

“Conectado”, “Reconectando” e perda das atualizações são estados distintos.
Uma queda de rede nunca oferece apagar credenciais ou ler um QR desnecessário.
Os POSTs manuais com CSRF continuam disponíveis para iniciar e renovar códigos
expirados. O QR nunca é publicado em logs ou em URL pública.

SSE ocupa um worker PHP enquanto a página estiver visível. Para separar esse uso
por operadores dos requests normais do Mautic, a implantação usa um pool FPM
exclusivo com quatro workers sob demanda e buffering desativado no Nginx.
Os exemplos estão em `deploy/whatsqr-sse-pool.conf` e `deploy/whatsqr-sse-nginx.conf`.
Ajuste os caminhos e replique as variáveis privadas necessárias no servidor;
não publique credenciais no repositório.

Envios novos por telefone consultam o identificador canônico no WhatsApp antes
de enviar. Números brasileiros com o nono dígito aceitam a variante antiga de oito
dígitos somente após a confirmação do próprio WhatsApp.
