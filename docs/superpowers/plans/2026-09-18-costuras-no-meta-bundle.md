# Costuras no MauticMetaBundle — plano de implementação

> **Para quem for executar:** SUB-SKILL OBRIGATÓRIA — use `superpowers:subagent-driven-development` (recomendada) ou `superpowers:executing-plans` para executar tarefa a tarefa. Os passos usam caixas (`- [ ]`) para acompanhamento.

**Objetivo:** abrir no `MauticMetaBundle` as costuras de que o canal por QR Code precisa, sem mudar o comportamento de nenhum dos três canais oficiais.

**Arquitetura:** o transporte HTTP do WhatsApp sai de dentro do `WhatsAppSender` para trás de uma interface de um método. Consentimento, política de cadência, log e conversa continuam exatamente onde estão. A fila ganha uma classe de falha temporária, ordem por conversa, e um backoff que sustenta horas.

**Pilha:** PHP 8.4, Symfony, Doctrine, PHPUnit. Repositório: `/Users/raphaelcangucu/projects/mautic-meta-bundle`.

**Plano 1 de 3.** O 2 é o serviço em Go; o 3 é o plugin e a caixa. Este entrega sozinho e não depende dos outros.

---

## Contexto que o executor precisa

Você provavelmente não conhece este código. Cinco coisas que vão te poupar horas:

**O `WhatsAppSender::send()` é quase todo agnóstico de transporte.** Ele faz, nesta ordem: guarda de conexão, guarda de asset, decide se é resposta dentro da janela de serviço, normaliza o telefone, `identities->assertCanSend()` (DNC, opt-out, opt-in), `outboundPolicy->assertAllowed()`, monta o payload, persiste um `MetaMessage`, e só então chama o Graph — **numa linha**. Depois trata a resposta e devolve um `WhatsAppSendResult`.

**Essa linha é tudo que muda de transporte.** Não mova mais nada para fora do `send()`. Se você se pegar copiando o `assertCanSend` para outro lugar, pare: é o erro que este plano existe para evitar.

**Não há definição de DI para o `WhatsAppSender`** — ele é autowired pela classe. Uma interface nova precisa de alias ou definição explícita em `Config/services.php`.

**`WhatsAppSender` é injetado em dois lugares** (`OutboundOperationExecutor` e `EventListener/CampaignSubscriber`). Nenhum dos dois muda neste plano: a classe continua concreta, só o transporte dela vira injetável.

**A `OutboundQueue` classifica falha por tipo de exceção**, e a taxonomia é do Graph: `DomainException` e `InvalidArgumentException` viram `failed` permanente; qualquer outra que não seja `MetaGraphApiException` retryable vira `uncertain`. Nenhuma das duas tenta de novo.

**A rede de segurança:** `Tests/Unit/Application/WhatsApp/WhatsAppSenderTest.php` e `Tests/Unit/Application/Queue/OutboundQueueTest.php` já existem. O critério de várias tarefas abaixo é que eles continuem verdes **sem uma linha editada**.

---

## Estrutura de arquivos

| Arquivo | Responsabilidade |
|---|---|
| `Infrastructure/WhatsAppTransportInterface.php` *(novo)* | Um método: leva o payload ao WhatsApp |
| `Infrastructure/GraphTransport.php` *(novo)* | A linha de hoje, extraída |
| `Infrastructure/TransportResolver.php` *(novo)* | Escolhe o transporte pelo tipo do asset |
| `Application/Exception/ChannelTemporarilyUnavailable.php` *(novo)* | Falha que volta sozinha |
| `Domain/AssetType.php` | Ganha o caso `whatsapp_qr_session` |
| `Application/WhatsApp/WhatsAppSender.php` | Usa o transporte; aceita o tipo novo |
| `Application/Queue/OutboundQueue.php` | Reconhece a falha temporária; backoff longo |
| `Entity/MetaOutboundJobRepository.php` | Ordem por conversa |
| `Config/services.php` | Alias da interface, tags do resolver |
| `Form/Type/MetaAssetType.php` | Lista o tipo novo |

---

## Tarefa 1: O transporte sai de dentro do sender

**Arquivos:**
- Criar: `Infrastructure/WhatsAppTransportInterface.php`, `Infrastructure/GraphTransport.php`
- Modificar: `Application/WhatsApp/WhatsAppSender.php`, `Config/services.php`
- Teste: `Tests/Unit/Infrastructure/GraphTransportTest.php`

Esta tarefa **não muda comportamento nenhum**. Se algum teste existente precisar de edição, você saiu do refactor puro — volte atrás.

- [ ] **Passo 1: Escrever o teste do transporte**

```php
public function testItPostsToTheAssetMessagesEdge(): void
{
    $graph = $this->createMock(MetaGraphClientInterface::class);
    $asset = $this->assetWithExternalId('55123');
    $graph->expects($this->once())->method('post')
        ->with($asset->getConnection(), '55123/messages', ['type' => 'text'])
        ->willReturn(['messages' => [['id' => 'wamid.1']]]);

    $resposta = (new GraphTransport($graph))->post($asset, ['type' => 'text']);

    self::assertSame('wamid.1', $resposta['messages'][0]['id']);
}
```

- [ ] **Passo 2: Rodar e ver falhar**

Expectativa: `Class "GraphTransport" not found`.

- [ ] **Passo 3: Criar a interface e a implementação**

```php
interface WhatsAppTransportInterface
{
    /** @param array<string,mixed> $payload @return array<string,mixed> */
    public function post(MetaAsset $asset, array $payload): array;
}
```

O `GraphTransport` recebe o `MetaGraphClientInterface` e faz exatamente o que a linha de hoje faz — inclusive montando o caminho `getExternalId().'/messages'`.

- [ ] **Passo 4: Trocar a linha no `send()`**

Só a linha. O `$send = fn (): array => ...` passa a chamar `$this->transport->post($asset, $payload)`. **Não mexa em mais nada do método.**

- [ ] **Passo 5: Registrar no `Config/services.php`**

Alias de `WhatsAppTransportInterface` para `GraphTransport`. Sem isso o autowire falha em runtime, e o teste unitário não pega porque ele constrói na mão.

- [ ] **Passo 6: Rodar os testes que já existiam, sem editar nenhum**

`WhatsAppSenderTest` e `OutboundQueueTest` precisam passar como estão. Se um deles pedir edição, o refactor não foi puro.

- [ ] **Passo 7: Commit**

---

## Tarefa 2: O tipo de asset novo

**Arquivos:**
- Modificar: `Domain/AssetType.php`, `Form/Type/MetaAssetType.php`
- Teste: `Tests/Unit/Domain/AssetTypeTest.php`

- [ ] **Passo 1: Escrever o teste**

```php
public function testTheQrSessionIsAWhatsAppChannel(): void
{
    self::assertSame(Channel::WhatsApp, AssetType::WhatsAppQrSession->channel());
}

public function testEveryCaseAnswersItsChannel(): void
{
    // O match de channel() e exaustivo: um caso sem braco estoura em runtime,
    // e nao na compilacao. Este teste e o que transforma isso em vermelho aqui.
    foreach (AssetType::cases() as $caso) {
        self::assertInstanceOf(Channel::class, $caso->channel());
    }
}
```

- [ ] **Passo 2: Rodar e ver falhar** — `UnhandledMatchError` no segundo teste.

- [ ] **Passo 3: Acrescentar o caso e o braço**

`case WhatsAppQrSession = 'whatsapp_qr_session';` e o braço junto dos outros dois de WhatsApp.

- [ ] **Passo 4: Listar no formulário de asset** — `Form/Type/MetaAssetType.php`.

- [ ] **Passo 5: Rodar e ver passar. Commit.**

---

## Tarefa 3: O sender aceita o tipo novo e escolhe o transporte

**Arquivos:**
- Criar: `Infrastructure/TransportResolver.php`
- Modificar: `Application/WhatsApp/WhatsAppSender.php`, `Config/services.php`
- Teste: `Tests/Unit/Infrastructure/TransportResolverTest.php`, mais um caso em `WhatsAppSenderTest`

- [ ] **Passo 1: O teste que mais importa deste plano inteiro**

```php
public function testAQrAssetStillGoesThroughConsent(): void
{
    // A regressao que a interface larga teria introduzido: o canal nao homologado
    // sendo o unico do sistema a ignorar quem pediu para sair.
    $this->identities->expects($this->once())->method('assertCanSend');

    $this->sender->sendText($this->qrAsset(), '5531999999999', 'oi', null, true, false);
}
```

- [ ] **Passo 2: O teste do resolvedor**

```php
public function testItPicksTheTransportByAssetType(): void
public function testAnAssetWithNoRegisteredTransportFails(): void
```

O segundo importa: sem ele, um tipo sem transporte devolveria `null` e estouraria com uma mensagem inútil três camadas adiante.

- [ ] **Passo 3: Rodar e ver falhar**

O primeiro falha com `InvalidArgumentException` da guarda de tipo — que é exatamente o que vamos abrir.

- [ ] **Passo 4: Abrir a guarda e ligar o resolvedor**

A guarda passa a aceitar `WhatsAppPhoneNumber` **e** `WhatsAppQrSession`. O `send()` pede o transporte ao resolvedor em vez de receber um só.

O resolvedor recebe os transportes por tag no contêiner, para o plugin novo registrar o dele sem editar este arquivo depois.

- [ ] **Passo 5: `WhatsAppSenderTest` e `OutboundQueueTest` verdes sem edição. Commit.**

---

## Tarefa 4: A falha que volta sozinha

**Arquivos:**
- Criar: `Application/Exception/ChannelTemporarilyUnavailable.php`
- Modificar: `Application/Queue/OutboundQueue.php`
- Teste: casos novos em `Tests/Unit/Application/Queue/OutboundQueueTest.php`

- [ ] **Passo 1: Escrever os testes**

```php
public function testATemporaryChannelFailureIsRetried(): void
public function testItDoesNotBecomeUncertain(): void
```

O segundo é o que prende o comportamento certo: hoje qualquer exceção que não seja `MetaGraphApiException` cai em `uncertain`, que **não tenta de novo** e que a caixa mostra como "não saiu".

- [ ] **Passo 2: Rodar e ver falhar** — o job vira `uncertain`.

- [ ] **Passo 3: Criar a exceção e reconhecê-la**

Reconhecimento ao lado do de `MetaGraphApiException` retryable, **antes** dos braços de `DomainException`/`InvalidArgumentException` — senão a hierarquia decide antes de você.

- [ ] **Passo 4: Rodar. Os testes antigos continuam verdes. Commit.**

---

## Tarefa 5: O backoff sustenta horas

**Arquivos:**
- Modificar: `Application/Queue/OutboundQueue.php`
- Teste: `OutboundQueueTest.php`

O backoff de hoje é `min(3600, 2^(n-1) * 30)` — teto de uma hora. Uma sessão de QR pode ficar caída mais que isso, e o número de tentativas precisa acompanhar.

- [ ] **Passo 1: Teste**

```php
public function testATemporaryFailureBacksOffBeyondAnHour(): void
public function testTheGraphBackoffIsUnchanged(): void
```

O segundo existe porque mexer no backoff mexe em três canais em produção. Ele é a prova de que não mexeu.

- [ ] **Passo 2: Rodar, implementar, rodar, commit.**

O teto maior vale **só** para `ChannelTemporarilyUnavailable`. O caminho do Graph fica idêntico.

---

## Tarefa 6: Ordem por conversa

**Arquivos:**
- Modificar: `Entity/MetaOutboundJobRepository.php`
- Teste: `Tests/Unit/Entity/MetaOutboundJobRepositoryTest.php` ou funcional, conforme o que o repositório já tenha

Hoje `findDue()` ordena por `availableAt ASC, id ASC`, sem serialização por conversa. Um job que entra em `retry` recebe `availableAt` no futuro e **passa para trás** de jobs criados depois.

- [ ] **Passo 1: O teste, com o cenário escrito por extenso**

```php
public function testAJobWaitsForAnEarlierJobOfTheSameConversation(): void
{
    // "Bom dia, Dona Marta" tropeca e reagenda; "seu pedido saiu hoje" e criada
    // depois e fica pronta antes. Sem esta regra o cliente le as duas ao contrario.
}

public function testJobsOfDifferentConversationsDoNotBlockEachOther(): void
```

O segundo é o que impede a correção de virar uma fila global de um por vez.

- [ ] **Passo 2: Rodar e ver falhar** — a segunda mensagem sai primeiro.

- [ ] **Passo 3: Implementar**

Não despachar job de uma conversa que tenha job anterior em estado não terminal. Cuidado com jobs **sem** conversa (campanha): eles não podem ficar presos atrás de nada.

- [ ] **Passo 4: Rodar. Commit.**

---

## Tarefa 7: A conexão de QR não aparece na tela do Meta

**Arquivos:**
- Modificar: o controlador/listagem de conexões do Meta
- Teste: funcional, ao lado de `Tests/Functional/MetaUiTest.php`

Um `MetaConnection` de QR tem `appId`, `businessId` e os três segredos vazios — a entidade os exige como `string` não-nulos. Deixá-lo aparecer na tela de Conexões mostra uma linha vazia com um botão *Testar conexão* que nunca vai funcionar.

**Decisão do spec: filtrar na tela.** Tornar os campos anuláveis mexe em código que atende três canais em produção, e o ganho não paga.

- [ ] **Passo 1: Teste** — a listagem não traz conexões de asset QR; traz todas as outras.
- [ ] **Passo 2: Rodar, implementar, rodar.**
- [ ] **Passo 3: Commit.**

---

## Critério de pronto, para o plano inteiro

1. `WhatsAppSenderTest` e `OutboundQueueTest` passam **sem nenhuma linha editada**. Este é o critério principal: ele é a prova de que os três canais em produção não mudaram.
2. Existe um teste que falha se alguém tirar o `assertCanSend` do caminho do asset QR.
3. Nenhum arquivo do `MauticInboxBundle` foi tocado.
4. `OutboundOperationExecutor` e `CampaignSubscriber` não foram tocados.

Se o item 3 ou o 4 não valer, algo saiu do escopo deste plano.
