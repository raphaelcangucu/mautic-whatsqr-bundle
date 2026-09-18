# WhatsApp por QR Code — desenho da etapa 1

**Data:** 2026-09-18
**Etapa:** 1 de 2. Esta entrega o número por QR Code na caixa de atendimento. A etapa 2, o disparo por campanha, tem desenho próprio e depende desta estar de pé.

> **Segunda versão.** A primeira foi revisada contra o código dos dois plugins existentes e vários pontos dela não se sustentaram. As correções estão marcadas com *(revisão)* onde mudam a decisão, não só a redação.

## O que estamos construindo

Um número comum de WhatsApp, pareado por QR Code, atendido pela mesma caixa que já atende os canais oficiais da Meta. O atendente não deveria precisar saber por qual caminho a mensagem chegou — o selo no avatar diz, e é só isso que muda para ele.

Três peças:

1. **Um serviço em Go** que mantém as sessões vivas com a biblioteca whatsmeow e expõe HTTP para o Mautic.
2. **O plugin `MauticWhatsQrBundle`**, que fala com esse serviço e entrega mensagens pelas costuras do `MauticMetaBundle`.
3. **Mudanças no `MauticMetaBundle` e no `MauticInboxBundle`.** *(revisão)* A primeira versão dizia que a caixa não mudava e que o plugin instalava sem editar o Meta bundle. As duas coisas eram falsas; a lista real está em *O que muda fora deste plugin*.

## Por que WhatsMeow, e não WPPConnect ou Baileys

| | Memória por sessão | Como fala com o WhatsApp |
|---|---|---|
| WPPConnect | Chromium inteiro, ~300–500 MB | Automatiza o WhatsApp Web |
| Baileys | ~60–100 MB de heap do Node | WebSocket, protocolo direto |
| **WhatsMeow** | **~20–40 MB, binário único** | WebSocket, protocolo direto |

O WPPConnect depende do DOM e do JavaScript interno do WhatsApp Web: quando a Meta muda a interface, ele quebra para todos ao mesmo tempo, sem aviso. Os outros dois só quebram quando o protocolo multi-device muda, o que é raro.

Entre Baileys e WhatsMeow pesou a memória e o fato de o serviço ser um binário estático, sem árvore de dependências para envelhecer. Contra, Go é língua nova nesta casa.

**O plugin se chama `MauticWhatsQrBundle`, não `MauticWhatsMeowBundle`.** O nome descreve a capacidade, não a biblioteca.

## O que este canal não é

- **Não é homologado.** Viola os termos do WhatsApp. O número pode ser banido, e o desenho assume que um dia será.
- **Não tem template homologado nem envio ativo em massa.** Isso é do WABA.
- **Não tem janela de 24 horas.** *(revisão)* A regra das 24h é do WABA. A caixa hoje a aplica a todo canal `whatsapp`, e isso precisa mudar.

## Arquitetura

### O serviço em Go

Processo único, `systemd` no mesmo servidor do Mautic, escutando em `127.0.0.1` — **nunca exposto à internet**. Até cinco sessões, uso interno.

Estado em SQLite ao lado do binário. Quem tem esse arquivo fala pelo número: permissão `0600`, dono do serviço, fora de qualquer diretório servido pelo nginx, tratado como credencial no backup. **O arquivo de configuração — token do serviço e segredo do webhook — recebe o mesmo tratamento.** *(revisão)* A primeira versão cuidava do SQLite e esquecia a configuração, que guarda o mesmo poder.

**API** *(revisão: enxugada)*:

| Rota | Faz |
|---|---|
| `POST /sessions` | Abre uma sessão e devolve `{id, status, qr}` |
| `GET /sessions/{id}/qr` | O QR renovado, só durante o pareamento |
| `DELETE /sessions/{id}` | Desconecta e apaga |
| `POST /sessions/{id}/messages` | Envia `{to, text, request_id}`, devolve `{message_id}` |
| `GET /health` | Estado de todas as sessões |

A primeira versão tinha `GET /sessions/{id}` devolvendo estado **e** QR, além de `/health` com o estado de todas — a mesma informação por dois caminhos. Com até cinco números, `/health` resolve a tela de Conexões e o QR vive só no pareamento.

Autenticação por token em cabeçalho **além** do bind local: o bind cai se alguém mudar a configuração, e o token sozinho não protege de outro processo no mesmo servidor.

**A fila de reenvio do webhook fica em memória, não em disco.** *(revisão)* A primeira versão dava ao serviço Go uma fila durável própria, com máquina de estados e testes. O Mautic já tem `meta_outbound_jobs` com claim atômico e backoff, e `meta_webhook_events` com dedupe por chave. Duplicar isso em Go é construir infraestrutura que já existe, numa língua que — como este documento admite — ninguém aqui lê. Buffer em memória com recuo exponencial; se o Mautic ficar fora do ar mais que o buffer, o que se perde é um evento de entrada que o próprio WhatsApp ainda tem.

### Entrada

O webhook verifica a assinatura, normaliza, e grava `MetaConversation` + `MetaMessage`.

**E chama `InboxIntegrationInterface::messagePersisted()`.** *(revisão)* A primeira versão dizia que gravar as entidades bastava e que "o SSE muda sozinho". Não existe listener do Doctrine: quem cria o `ConversationState`, marca `needsResponse`, grava o `EventLog` e enfileira o push é aquela chamada, que o Meta bundle faz explicitamente no seu processador de webhook. Sem ela, a conversa não aparece em fila nenhuma e não gera push.

**O que vira `recipient` de uma conversa QR.** *(revisão)* O whatsmeow entrega JID (`5511999999999@s.whatsapp.net`) e, nas versões recentes, às vezes um identificador opaco de privacidade no lugar do telefone. A regra:

- JID com telefone → o telefone normalizado pelo `PhoneNormalizer`. O tratamento do nono dígito brasileiro que ele já faz é exatamente o que este canal precisa.
- JID opaco → **a conversa é criada, marcada como sem telefone resolvido, e o compositor fica fechado com motivo escrito.** Não inventamos um `recipient` que não casa com contato nenhum: isso produziria uma conversa que aparece, aceita resposta, e falha em definitivo no envio — porque `normalize()` lança `InvalidArgumentException`, que a fila classifica como falha permanente.

## A costura de saída

*(revisão: esta seção mudou de decisão.)*

A primeira versão propunha transformar `WhatsAppSender` em interface, com `GraphWhatsAppSender` e `QrWhatsAppSender` lado a lado. Ao ler o `send()` inteiro, isso se mostrou errado.

O método faz, antes de tocar no Graph: guarda de conexão e de asset, normalização do telefone, **`identities->assertCanSend()`** (DNC, opt-out, opt-in), **`outboundPolicy->assertAllowed()`** (cadência e limite), persistência do `MetaMessage` que a caixa lê, e a guarda de automação. O trecho específico do Graph é **uma linha**.

Trocar o sender inteiro obrigaria o `QrWhatsAppSender` a reimplementar tudo isso, e a primeira coisa perdida na prática seria o `assertCanSend`. O canal **não homologado**, cujo risco central é banimento, seria o único do sistema a ignorar "SAIR" e DNC — enquanto o processador de webhook continua registrando opt-out por palavra-chave na entrada. O sistema anotaria o opt-out e mandaria assim mesmo.

**A costura certa é o transporte, não o sender:**

```php
interface WhatsAppTransportInterface
{
    public function post(MetaAsset $asset, array $payload): array;
}
```

`GraphTransport` é a linha de hoje. `QrTransport` traduz o payload do Graph para a chamada do serviço — para `type: text`, uma função de dez linhas. O `send()` escolhe pelo tipo do asset. Consentimento, política, log, conversa e adaptadores ficam onde estão.

De brinde: `QrTransport` não precisa saber `sendTemplate`, `sendMedia` nem `sendInteractive` — operações que o canal não sabe fazer e que, com a interface larga, seriam métodos vazios esperando para serem chamados por engano.

**O `match` do `OutboundOperationExecutor` continua sem mudar.** Isso é verdade e é menos importante do que a primeira versão vendia.

## O motor é escolhido por número

WhatsMeow é a escolha de hoje, não uma premissa. Canal não homologado é território onde bibliotecas morrem.

**E a escolha é por número.** Num canal não homologado o comportamento varia por número: um que comece a cair com frequência precisa poder mudar de motor sozinho, sem mexer nos outros quatro nem derrubar o atendimento inteiro.

```php
interface SessionDriverInterface
{
    public function openSession(MetaAsset $asset): SessionState;
    public function pairingQr(MetaAsset $asset): ?string;
    public function closeSession(MetaAsset $asset): void;
    public function sendText(MetaAsset $asset, string $to, string $text, string $requestId): SentMessage;
}
```

Quatro métodos, cada um porque o plugin **já precisa dele hoje**. `SessionState` e `SentMessage` são objetos do plugin: o adaptador traduz, e o dialeto de cada motor não vaza para dentro.

`SessionDriverFactory::forAsset()` lê o motor gravado naquele número e devolve o adaptador com o endereço e o token daquele número. A configuração global guarda só o **padrão para números novos**.

| Onde | Campo |
|---|---|
| Configuração do plugin | Motor padrão: `whatsmeow` · `baileys` (não implementado) |
| Em cada número | Motor · endereço · token · segredo do webhook · **JID pareado** |

Escolher um motor sem implementação **recusa na hora de configurar**, não na hora de enviar com um cliente esperando.

Não vou desenhar a interface tentando antecipar o Baileys. Quando ele chegar é provável que esses quatro métodos mudem; com adaptador e fábrica no lugar, a mudança é local e tem teste dizendo o que quebrou.

## Segurança do webhook

**Reusar o `WebhookSignatureVerifier` que já existe** no Meta bundle — ele já faz `hash_equals` sobre `sha256=…`. *(revisão)* Dizer só "assinado com HMAC-SHA256", como a primeira versão fazia, é como nasce um `===` de string que vaza o segredo byte a byte para quem medir o tempo das respostas.

Com segredo por número, chega um problema que segredo único não tinha: qual chave usar para conferir? A única pista é o corpo, que é o que a assinatura existe para provar. A solução é o serviço dizer qual chave usou:

```
X-WhatsQr-Key: <id da conexão, não enumerável>
X-WhatsQr-Timestamp: <epoch>
X-WhatsQr-Signature: <HMAC-SHA256 de timestamp + corpo>
```

O `Key` não é segredo: ele só **seleciona** a chave. Quem prova é a assinatura. Id sequencial entregaria a topologia dos números a qualquer um que bata na rota.

**A assinatura cobre o timestamp, e fora de cinco minutos é recusado.** *(revisão)* Sem isso, um corpo assinado capturado uma vez vale para sempre. A deduplicação por id da mensagem só cobre `message`; `status` e `session` não têm id de mensagem, então **todo evento recebe id próprio** e passa pelo dedupe que o `WebhookIngestor` já faz por chave. Sem isso, reenviar em laço um `session: logged_out` mantém o canal marcado como caído indefinidamente — negação de serviço sem tocar no serviço Go.

**O que o segredo compra se vazar**, e isso muda a classificação do risco: não é "mensagem falsa na tela". Uma entrada forjada passa por `messagePersisted()`, dispara campanha e aciona a IA da caixa. Ou seja, compra **fazer o seu número não homologado enviar para um destinatário escolhido pelo atacante** — o caminho mais curto para o risco central deste documento. Por isso o segredo é por número e não global, e por isso um `message` de remetente sem conversa existente entra marcado para revisão.

## A sessão vai cair

Não é exceção, é rotina. A Meta derruba, o aparelho fica sem internet, alguém desconecta pelo celular, catorze dias fora do ar desfazem o pareamento.

**O estado "reconectando" mora em `settings` do asset, não em `status`.** *(revisão)* A primeira versão dizia "o plugin marca o asset" e "o compositor continua aberto". As duas não podem ser verdadeiras: o `ReplyAvailability` fecha o compositor quando o asset sai de `active`, e o `retry` passa a devolver 409.

**A fila que a primeira versão descrevia não existe.** *(revisão)* O envio do atendente é enfileirado com `maxAttempts = 1` e executado **dentro da requisição HTTP**. A `OutboundQueue` classifica falha por tipo de exceção, com taxonomia do Graph: `DomainException` e `InvalidArgumentException` viram `failed` permanente; qualquer outra que não seja `MetaGraphApiException` vira `uncertain`, que também não tenta de novo. Com a sessão caída, o atendente veria **"não saiu" em dois segundos** — exatamente o estado que este documento diz não poder ser confundido com "na fila".

Para o comportamento desejado existir, estas peças precisam ser construídas:

1. **Uma exceção `ChannelTemporarilyUnavailable`** que a `OutboundQueue` reconheça como retryable, ao lado da `MetaGraphApiException`.
2. **`maxAttempts` maior para asset QR** no enfileiramento do envio humano.
3. **`availableAt` que sustente horas** — o backoff de hoje teto em uma hora.
4. **Um comando que expire** jobs de número desconectado em duas horas, passando-os a *não saiu* com o motivo escrito. Responder "já vou verificar" seis horas depois é pior que não responder.

O vocabulário visual já existe e não precisa ser inventado: a bolha da caixa já distingue `queued → "na fila"`, `waiting → "aguardando nova tentativa"` e `failed → "não saiu"`.

**Ordem das mensagens.** *(revisão)* Um job que entra em `retry` recebe `availableAt` no futuro e **passa para trás** de jobs criados depois; não há serialização por conversa. O atendente manda "Bom dia, Dona Marta" e, quinze segundos depois, "seu pedido saiu hoje"; a primeira tropeça e a segunda sai antes. No Graph isso é raro porque retry é raro — aqui cair é rotina. **A fila passa a ser FIFO por conversa:** não despachar job de uma conversa que tenha job anterior em estado não terminal.

**Voltar como outro número.** *(revisão)* O JID pareado é gravado no asset. Reconexão com JID diferente é **recusada**, e a tela pede um asset novo. Sem essa regra: o número é banido na sexta, na segunda alguém pareia outro chip reaproveitando o asset, e os jobs que estavam na fila saem pelo número novo — o cliente recebe "conforme combinamos" de um número que nunca viu, sem histórico no aparelho.

## Mídia: fora de escopo, mas visível

*(revisão)* A primeira versão dizia só "entra depois", sem responder o que o atendente vê. As duas saídas naturais são ruins: ignorar o evento faz a mensagem não existir — o cliente manda a foto do boleto e escreve "é esse aqui", e o atendente lê só o "é esse aqui" —, e gravar com o id da mídia produz anexo quebrado, porque o download passa pelo Graph e não há credencial Graph aqui.

**A caixa já tem a saída pronta:** `messageType = 'unsupported'`, que ela renderiza com um texto explicando que o conteúdo não veio e pedindo para reenviar. Falta só um texto específico para este canal. Isso transforma "fora do escopo" em degradação **conhecida e visível**, e custa quase nada.

## O que muda fora deste plugin

*(revisão: esta seção não existia, e a sua ausência era o maior erro da primeira versão.)*

**No `MauticMetaBundle`:**

- `AssetType` ganha o caso `whatsapp_qr_session` e o braço correspondente no `match` de `channel()` — que é exaustivo e estouraria em runtime sem ele. **Instalar o plugin exige release do Meta bundle.** Não é plug-and-play, e prometer isso era falso.
- `WhatsAppTransportInterface` + `GraphTransport`, e o `send()` escolhendo o transporte.
- A guarda `AssetType::WhatsAppPhoneNumber !== $asset->getType()` no `send()` passa a aceitar o tipo novo.
- `ChannelTemporarilyUnavailable` e o reconhecimento dela na `OutboundQueue`.
- FIFO por conversa no repositório de jobs.
- `MetaAssetType` (formulário) lista o tipo novo.
- **O que é um `MetaConnection` de QR:** a entidade exige `appId`, `businessId` e três segredos como `string` não-nulos. Uma conexão QR é uma linha com esses campos vazios, que apareceria na tela de Conexões do Meta e no botão *Testar conexão*. **Decisão: filtrar na tela**, porque tornar os campos anuláveis mexe em código que atende três canais em produção.

**No `MauticInboxBundle`:**

- `ReplyAvailability` **não aplica a janela de 24h** a asset QR, e lê o estado de reconexão de `settings` em vez de `status`.
- O caminho de template recusa asset QR com mensagem própria, em vez do `template.whatsapp_only` genérico.
- O payload da conversa carrega o tipo do asset, e a lista desenha o selo `WhatsApp · QR`. Pequeno, mas é código.

## Telas

Desenhadas e aprovadas em https://claude.ai/artifact/XKQWTWepYCAwqe38GA8tKi

**Conexões** — os números, a situação, quantas respostas estão na fila, e a última mensagem. A coluna da fila é como alguém descobre que três clientes estão esperando.

**Parear** — um componente, três estados. O estado "não deu" distingue *expirou* de *o WhatsApp recusou*, e só o primeiro oferece tentar de novo.

**A caixa** — sem tela nova. Selo no avatar e linha de contexto `WhatsApp · QR · <nome do número>`.

**Queda** — a faixa na conversa, as bolhas na fila, o compositor aberto dizendo para onde o texto vai.

## Testes

**Serviço em Go:** máquina de estados da sessão e o buffer de reenvio. O whatsmeow entra por interface, para o teste não precisar de um WhatsApp de verdade.

**Plugin:** webhook — assinatura válida grava; inválida recusa; **corpo assinado com a chave de outro número é recusado**; timestamp velho é recusado; evento repetido não duplica. Fábrica — dois números com motores diferentes devolvem adaptadores diferentes; motor sem implementação recusa na configuração. Expiração da fila. JID diferente recusa reconexão.

**Meta bundle:** a extração do transporte é refactor puro, e o critério é que **os testes existentes continuem verdes sem edição**. Mais um teste novo: um envio por asset QR passa por `assertCanSend` — a regressão que a interface larga teria introduzido merece um teste que a impeça de voltar.

**O que nenhum teste pega**, e precisa ser feito no aparelho: parear, mandar e receber, tirar o celular da internet e ver a fila segurar, reconectar e ver a fila sair na ordem certa.

## Fora do escopo desta etapa

- **Mídia** — degradação visível, conforme acima; envio e recepção de verdade ficam para depois.
- **Grupos.** A caixa não tem conceito de conversa com vários participantes.
- **Disparo por campanha.** Etapa 2.
- **O adaptador do Baileys.** Interface e fábrica entram agora; a implementação, não. E a fábrica troca o adaptador PHP — um motor Baileys exigiria também um serviço Node novo.
- **Vários clientes no mesmo serviço.** Cinco números, todos de vocês.

## Riscos, sem eufemismo

**O número pode ser banido.** Não tem mitigação técnica completa. O que dá para fazer: não usar este canal para disparo em massa na etapa 1, respeitar cadência humana, manter o histórico no Mautic para que perder o número não signifique perder o atendimento — e **não perder o `assertCanSend`**, que é o que impede o sistema de mandar para quem pediu para sair.

**Go é língua nova aqui.** Mitigação: manter o serviço burro. Ele não decide nada de negócio, só mantém a sessão e repassa. A revisão já cortou dali a fila durável por esse motivo.

**O arquivo de sessão e o de configuração falam pelo número.** Permissão restrita, fora de diretório servido, tratados como credencial no backup.

**Este documento já errou uma vez.** A primeira versão afirmava quatro coisas que o código desmentia, e todas na direção otimista — o trabalho parecendo menor do que é. O plano que sair daqui deve assumir que ainda há afirmações minhas não verificadas, e verificar antes de implementar, não depois.
