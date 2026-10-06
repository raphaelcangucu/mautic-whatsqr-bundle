# Mautic WhatsApp QR

Plugin de conexão de números WhatsApp por QR para o Inbox multicanal do Mautic.
O serviço Go mantém a sessão com Whatsmeow; o plugin autentica os webhooks e usa
as conversas, contatos, consentimentos e fila do conector Meta.

Versão publicada: **0.2.1**, incluindo o plugin PHP e o serviço Go.

## Dependências

- Mautic 7, PHP 8.2 ou superior e [MauticMetaBundle 0.14.2](https://github.com/raphaelcangucu/mautic-meta-bundle/releases/tag/v0.14.2) ou compatível.
- [MauticInboxBundle 1.4.1](https://github.com/raphaelcangucu/mautic-inbox-bundle/releases/tag/v1.4.1) ou compatível, para atendimento.
- Go 1.26 somente para compilar; binário estático Linux/amd64 em produção.
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

## Serviço

Compilar em `service/`:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o whatsqr .
```

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

## Limites desta primeira versão

- Recepção de mensagens privadas e envio de texto. Grupos, status e newsletters
  não são convertidos em atendimentos privados.
- Mídia recebida aparece como conteúdo não suportado; não há download, prévia nem
  envio de anexos neste transporte.
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
