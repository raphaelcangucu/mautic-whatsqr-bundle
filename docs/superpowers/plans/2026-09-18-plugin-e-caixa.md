# O plugin e a caixa — plano de implementação

> **Para quem for executar:** SUB-SKILL OBRIGATÓRIA — use `superpowers:subagent-driven-development` ou `superpowers:executing-plans`.

**Objetivo:** o número por QR Code aparecendo e sendo atendido na mesma caixa dos canais oficiais, com a queda da sessão tratada como rotina.

**Arquitetura:** o plugin fala com o serviço em Go por um adaptador escolhido **por número**; a entrada vira `MetaConversation` + `MetaMessage` e acorda a caixa pela chamada que o Meta bundle já usa; a saída passa pelo transporte aberto no plano 1, preservando consentimento e política.

**Pilha:** PHP 8.4, Symfony, Doctrine, Svelte 5. Repositórios: `mautic-whatsqr-bundle` (novo), `mautic-inbox-bundle` (mudanças pontuais).

**Plano 3 de 3.** Depende do 1 (costuras) e do 2 (serviço em Go) estarem prontos.

---

## Um bloqueio conhecido, e é melhor resolver antes de começar

Os testes funcionais do Mautic usam `MauticMysqlTestCase`, que chama `mysqldump`. **O `mariadb-client` não está instalado no servidor**, e por isso a tarefa 6 do trabalho anterior — o `reply` devolvendo o item criado — está em produção sem nunca ter rodado um teste.

Várias tarefas abaixo têm teste funcional. Instalar precisa de sudo:

```bash
sudo apt-get install -y mariadb-client
```

Se não for resolvido, essas tarefas entregam o teste escrito e **não executado**, e isso precisa estar dito no relatório de cada uma — não descoberto depois.

---

## Estrutura de arquivos

**No plugin novo:**

| Arquivo | Responsabilidade |
|---|---|
| `Config/config.php` | Rotas, versão, menu |
| `Domain/SessionState.php`, `Domain/SentMessage.php` | Os objetos do plugin, não a resposta crua do serviço |
| `Driver/SessionDriverInterface.php` | Os quatro métodos |
| `Driver/WhatsMeowDriver.php` | O único motor implementado |
| `Driver/SessionDriverFactory.php` | Escolhe pelo número |
| `Transport/QrTransport.php` | Registrado no resolvedor do plano 1 |
| `Controller/WebhookController.php` | Seleção de chave, assinatura, replay, dedupe |
| `Application/InboundIngestor.php` | Grava e chama `messagePersisted()` |
| `Controller/ConnectionController.php` | As telas de Conexões e Parear |
| `Command/ExpireQueuedCommand.php` | As duas horas |

**No inbox:** `Application/ReplyAvailability.php`, `Application/InboxQuery.php`, `Application/WhatsAppTemplates.php`, `Frontend/inbox/ConversationList.svelte`.

---

## Tarefa 1: O esqueleto do plugin

- [ ] Criar `MauticWhatsQrBundle.php`, `Config/config.php`, `composer.json`, a rota pública do webhook.
- [ ] **Passo de verificação:** o Mautic lista o plugin em Configurações e não quebra nenhuma página. Um plugin que impede o painel de abrir é o pior primeiro dia possível.
- [ ] Commit.

---

## Tarefa 2: Os objetos e o adaptador

**Teste:** `Tests/Unit/Driver/WhatsMeowDriverTest.php`

- [ ] **Passo 1: Testes, com a rede injetada**

```php
public function testOpenSessionReturnsPairingWithAQr(): void
public function testItTranslatesTheServiceDialectIntoSessionState(): void
public function testASendReturnsTheMessageId(): void
public function testAnUnreachableServiceRaisesChannelTemporarilyUnavailable(): void
```

O último é a cola com o plano 1: serviço fora do ar **precisa** virar a falha temporária. Se virar qualquer outra coisa, a fila classifica como permanente e o atendente vê "não saiu" em dois segundos — que é exatamente o defeito que este trabalho existe para não ter.

- [ ] **Passo 2: Rodar, implementar, rodar. Commit.**

---

## Tarefa 3: A fábrica olha o número

**Teste:** `Tests/Unit/Driver/SessionDriverFactoryTest.php`

- [ ] **Passo 1: Testes**

```php
public function testTwoNumbersWithDifferentEnginesGetDifferentDrivers(): void
public function testAnEngineWithNoImplementationIsRefused(): void
public function testTheRefusalHappensWhenConfiguring(): void
```

O terceiro é a regra: recusar **na configuração**, nunca na hora de enviar com um cliente esperando.

- [ ] **Passo 2: Rodar, implementar, rodar. Commit.**

---

## Tarefa 4: O webhook, que é a superfície pública

**Teste:** `Tests/Functional/WebhookTest.php`

Esta é a rota exposta à internet. Os testes vêm antes por isso.

- [ ] **Passo 1: Testes**

```php
public function testAValidSignatureIsAccepted(): void
public function testAnInvalidSignatureIsRefused(): void
public function testABodySignedWithAnotherNumbersKeyIsRefused(): void
public function testAnOldTimestampIsRefused(): void
public function testTheSameEventTwiceIsStoredOnce(): void
public function testAnUnknownKeyIdIsRefusedWithoutReadingTheBody(): void
```

O terceiro é o que impede um serviço comprometido de falar por qualquer número. O quarto fecha o replay: sem ele, um `session: logged_out` capturado uma vez e reenviado em laço mantém o canal marcado como caído para sempre.

- [ ] **Passo 2: Rodar e ver falhar.**
- [ ] **Passo 3: Implementar**

Reusar o `WebhookSignatureVerifier` do Meta bundle — ele já faz `hash_equals`. **Não escreva outro**; é ali que nasce um `===` de string que vaza o segredo pelo tempo de resposta.

A ordem importa: ler o cabeçalho `X-WhatsQr-Key` → achar a conexão → conferir assinatura sobre timestamp+corpo → **só então** ler o corpo.

- [ ] **Passo 4: Rodar. Commit.**

---

## Tarefa 5: A entrada acorda a caixa

**Teste:** `Tests/Functional/InboundIngestorTest.php`

- [ ] **Passo 1: Testes**

```php
public function testItCreatesTheConversationAndTheMessage(): void
public function testItCallsMessagePersisted(): void
public function testAJidWithoutAPhoneCreatesAConversationThatCannotReply(): void
public function testMediaBecomesAnUnsupportedMessage(): void
```

O segundo é o achado da revisão: **gravar as entidades não avisa ninguém.** Sem `messagePersisted()`, a conversa não entra em fila nenhuma e não gera push.

O terceiro trata o JID opaco: conversa criada, marcada, compositor fechado com motivo. Inventar um destinatário produziria uma conversa que aceita resposta e falha em definitivo no envio.

O quarto é a degradação de mídia: `messageType = 'unsupported'`, que a caixa **já sabe renderizar** com um texto pedindo para reenviar. O cliente manda a foto do boleto e o atendente ao menos vê que veio algo.

- [ ] **Passo 2: Rodar, implementar, rodar. Commit.**

---

## Tarefa 6: A saída, pelo transporte do plano 1

**Teste:** `Tests/Unit/Transport/QrTransportTest.php`

- [ ] **Passo 1: Testes**

```php
public function testItTranslatesAGraphTextPayload(): void
public function testATemplatePayloadIsRefusedWithAClearReason(): void
```

O segundo existe porque o canal não sabe mandar template, e a recusa precisa dizer isso — não estourar com erro de chave faltando.

- [ ] **Passo 2: Registrar no resolvedor do plano 1**, por tag no contêiner.
- [ ] **Passo 3: Rodar, implementar, rodar. Commit.**

---

## Tarefa 7: As duas horas

**Teste:** `Tests/Functional/ExpireQueuedCommandTest.php`

- [ ] **Passo 1: Testes**

```php
public function testAJobQueuedForOverTwoHoursBecomesFailed(): void
public function testTheReasonSaysTheNumberWasDisconnected(): void
public function testAJobOfAConnectedNumberIsLeftAlone(): void
```

- [ ] **Passo 2: Rodar, implementar, rodar.**
- [ ] **Passo 3: Agendar** junto dos comandos que o Mautic já roda. Um comando que ninguém agenda é código morto.
- [ ] **Passo 4: Commit.**

---

## Tarefa 7b: O `maxAttempts`, e uma garantia que não pode ser atropelada

> **Descoberto durante o plano 1.** A tarefa 5 de lá mediu: com `maxAttempts = 1`, o primeiro fracasso já bate o teto e o job vira `failed` **antes de qualquer reagendamento**. Ou seja, o backoff de duas horas que acabamos de construir é **inerte** até alguém subir esse número — e quem o define é a caixa, não a fila.

Três lugares passam `1` hoje:

| Onde | O quê |
|---|---|
| `ConversationActions.php` ~295 | O envio do atendente |
| `ConversationActions.php` ~242 | O **retry manual** |
| `Ai/AiWorker.php` | O envio da IA |

**O retry manual tem um teste que afirma `maxAttempts = 1`** — `Tests/Functional/InboxPersistenceTest.php:274`. Isso é garantia deliberada de quem escreveu, não descuido: o atendente aperta "tentar de novo" e espera uma tentativa, não uma série silenciosa.

- [ ] **Passo 1: Teste** — o envio do atendente **para asset QR** enfileira com `maxAttempts` suficiente para atravessar duas horas; para asset oficial continua `1`.
- [ ] **Passo 2: Teste que o retry manual continua `1`** para os dois tipos. Se o teste existente do `InboxPersistenceTest` precisar mudar, **pare**: a garantia é de outra pessoa e mudá-la é decisão dela.
- [ ] **Passo 3: Rodar, implementar, rodar.**
- [ ] **Passo 4: A IA fica de fora** — resposta de IA parada duas horas não deve sair sozinha depois. Confirme que não mudou.
- [ ] **Passo 5: Commit.**

---

## Tarefa 8: As mudanças na caixa

**Arquivos:** `mautic-inbox-bundle` — `Application/ReplyAvailability.php`, `Application/InboxQuery.php`, `Application/WhatsAppTemplates.php`, `Frontend/inbox/ConversationList.svelte`

Quatro mudanças pequenas e uma regra: **a suíte inteira do inbox precisa continuar verde.** São 72 testes, e eles cobrem a tela que sua equipe usa todo dia.

- [ ] **Passo 1: Testes, antes de tocar em qualquer coisa**

```php
public function testTheTwentyFourHourWindowDoesNotApplyToAQrAsset(): void
public function testAReconnectingAssetStillAllowsReplies(): void
public function testTheOfficialWhatsAppWindowIsUnchanged(): void
public function testTheTemplatePathRefusesAQrAssetWithItsOwnReason(): void
```

O terceiro é o guarda: a janela de 24h é regra do WABA e **precisa continuar valendo** para os números oficiais. Mexer nela sem esse teste quebra o canal homologado em silêncio.

- [ ] **Passo 2: Rodar e ver falhar.**
- [ ] **Passo 3: Implementar**

- `ReplyAvailability` não aplica a janela a asset QR, e lê "reconectando" de `settings` — **nunca de `status`**, porque sair de `active` fecha o compositor e faz o `retry` devolver 409.
- `InboxQuery` acrescenta o tipo do asset ao payload da conversa. Hoje ele manda `id`, `name`, `handle` e `phone`, e nada mais — confirmado no código.
- `WhatsAppTemplates` recusa asset QR com mensagem própria.

- [ ] **Passo 4: O selo, no Svelte**

`ConversationList.svelte` desenha `WhatsApp · QR` quando o tipo for de sessão. `npm test` verde.

- [ ] **Passo 5: A suíte inteira do inbox verde. Commit.**

---

## Tarefa 9: As telas de Conexões e Parear

**Desenho aprovado:** https://claude.ai/artifact/XKQWTWepYCAwqe38GA8tKi

- [ ] **Passo 1: Conexões** — número, situação, **quantas respostas estão na fila**, última mensagem. A coluna da fila não é decoração: é como alguém descobre que três clientes estão esperando.
- [ ] **Passo 2: Parear** — um componente, três estados. O estado "não deu" distingue **expirou** de **o WhatsApp recusou**; só o primeiro oferece tentar de novo.
- [ ] **Passo 3: Conferir em 390px de largura.** A tela de Conexões vai ser aberta do celular quando um número cair, que é justamente quando ninguém está na frente do computador.
- [ ] **Passo 4: Commit.**

---

## Tarefa 10: Ponta a ponta, na corrente inteira

Os E2E do plano 2 provaram o serviço sozinho. Estes provam Mautic incluído.

- [ ] **E2E 1** — parear pela tela de Conexões, não por `curl`. O QR aparece, o aparelho escaneia, a tela vira "conectado" **sozinha**.
- [ ] **E2E 2** — mandar uma mensagem de outro celular. *Conferir:* aparece na caixa junto das conversas dos canais oficiais, com o selo `QR`, e **o push chega no aparelho**. O push é o que prova que o `messagePersisted()` foi chamado de verdade.
- [ ] **E2E 3** — responder pela caixa, no celular. *Conferir:* a bolha aparece na hora (a UI otimista vale para este canal também) e a mensagem chega no outro aparelho.
- [ ] **E2E 4 — o mais importante.** Pôr o aparelho pareado em modo avião. Responder pela caixa. *Conferir:* a bolha diz **"na fila"**, e não "não saiu". Tirar do modo avião. *Conferir:* sai sozinha, e **na ordem certa** se houver mais de uma.
- [ ] **E2E 5** — mandar uma **foto**. *Conferir:* aparece como mensagem não suportada com o texto explicando, e não some nem vira anexo quebrado.
- [ ] **E2E 6 — o que protege quem pediu para sair.** Marcar um contato como opt-out e tentar responder por um número QR. *Conferir:* **recusado**. Este é o teste que prova que o `assertCanSend` não se perdeu no caminho — a regressão que a interface larga teria introduzido.
- [ ] **Passo final: escrever o resultado de cada um**, com data e número usado.

---

## Critério de pronto, para o projeto inteiro

1. Os 72 testes do inbox verdes, e a suíte do Meta bundle verde.
2. Um número real pareado, recebendo e respondendo pela caixa.
3. A queda testada de verdade: modo avião, bolha "na fila", volta na ordem.
4. Opt-out recusado num número QR.
5. Os resultados dos E2E escritos, com data.
6. A janela de 24h ainda valendo para os números oficiais.
