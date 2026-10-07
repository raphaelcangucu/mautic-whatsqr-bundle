<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;

use MauticPlugin\MauticWhatsQrBundle\Application\InboundIngestor;
use MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Support\InboundIngestorFixture;
use PHPUnit\Framework\TestCase;

/**
 * O que a entrada tem de fazer, e na ordem.
 *
 * O achado da revisao do desenho esta no segundo teste: gravar as entidades nao avisa
 * ninguem. Nao existe listener do Doctrine -- quem cria o estado da conversa, marca que
 * ela precisa de resposta, grava o log e enfileira o push e a chamada explicita a
 * `messagePersisted()`. Sem ela a caixa fica em silencio com mensagem de cliente dentro,
 * e nenhum teste de "gravou a mensagem" perceberia isso.
 */
final class InboundIngestorTest extends TestCase
{
    use InboundIngestorFixture;

    public function testPhoneReplyIsOutboundAndDoesNotCreateCustomerActivityOrRunAutomation(): void
    {
        $asset = $this->qrAsset();
        $ingestor = $this->inboundIngestor();
        $first = $ingestor->ingest($asset, $this->messageEvent('5511999999999@s.whatsapp.net', 'pergunta', false, 'INCOMING'));
        $conversation = $first->getConversation();
        $lastInbound = $conversation->getLastInboundAt();
        $this->inboxCalls = [];
        $this->identityCalls = [];

        $event = $this->messageEvent('5511999999999@s.whatsapp.net', 'SAIR', false, 'PHONE-REPLY');
        $event['message']['from_me'] = true;
        $event['message']['name'] = 'Nome do atendente';
        $event['message']['timestamp'] = time() - 5;
        $message = $ingestor->ingest($asset, $event);

        self::assertSame('outbound', $message->getDirection());
        self::assertSame('sent', $message->getStatus());
        self::assertSame($conversation, $message->getConversation());
        self::assertSame(1, $conversation->getUnreadCount());
        self::assertSame($lastInbound, $conversation->getLastInboundAt());
        self::assertSame([], $this->identityCalls);
        self::assertNull($message->getPayload()['whatsqr']['consent_keyword']);
        self::assertArrayNotHasKey('contact', $message->getPayload());
        self::assertSame(['messagePersisted'], array_column($this->inboxCalls, 'call'));
        self::assertSame($event['message']['timestamp'], $message->getDateAdded()->getTimestamp());
        self::assertNull($ingestor->ingest($asset, $event));
        self::assertCount(2, $this->persistedMessages());
        self::assertCount(1, $this->inboxCalls);
    }

    public function testPhoneAttachmentUsesTheSamePrivateMediaReference(): void
    {
        $event = $this->messageEvent('5511999999999@s.whatsapp.net', 'foto enviada no celular');
        $event['message']['from_me'] = true;
        $event['message']['attachment'] = ['type' => 'image', 'id' => str_repeat('a', 64), 'file_size' => 200, 'mime_type' => 'image/png'];
        $message = $this->inboundIngestor()->ingest($this->qrAsset(), $event);
        self::assertSame('image', $message->getMessageType());
        self::assertSame('outbound', $message->getDirection());
        self::assertSame(str_repeat('a', 64), $message->getPayload()['message']['image']['id']);
        self::assertSame(0, $message->getConversation()->getUnreadCount());
        self::assertNull($message->getConversation()->getLastInboundAt());
    }

    public function testOldHistoryDoesNotReorderRecentChatCreateUnreadAlertsOrRunConsentAndAutomation(): void
    {
        $asset = $this->qrAsset();
        $ingestor = $this->inboundIngestor();
        $new = $ingestor->ingest($asset, $this->messageEvent('5511999999999@s.whatsapp.net', 'hoje', false, 'LIVE'));
        $conversation = $new->getConversation();
        $lastMessage = $conversation->getLastMessageAt();
        $lastInbound = $conversation->getLastInboundAt();
        $conversation->setStatus('resolved');
        $this->identityCalls = $this->inboxCalls = [];
        $event = $this->messageEvent('5511999999999@s.whatsapp.net', 'SAIR', false, 'OLD');
        $event['message']['historical'] = true;
        $event['message']['timestamp'] = time() - 86400 * 30;
        $old = $ingestor->ingest($asset, $event);
        self::assertSame($conversation, $old->getConversation());
        self::assertSame($event['message']['timestamp'], $old->getDateAdded()->getTimestamp());
        self::assertSame($lastMessage, $conversation->getLastMessageAt());
        self::assertSame($lastInbound, $conversation->getLastInboundAt());
        self::assertSame('resolved', $conversation->getStatus());
        self::assertSame(1, $conversation->getUnreadCount());
        self::assertSame([], $this->identityCalls);
        self::assertSame([], $this->inboxCalls);
        self::assertNull($ingestor->ingest($asset, $event));
    }

    public function testNewHistoricalConversationIsQuietAndHasItsOriginalDate(): void
    {
        $event = $this->messageEvent('5511999999999@s.whatsapp.net', 'ontem');
        $event['message']['historical'] = true;
        $event['message']['timestamp'] = time() - 86400;
        $message = $this->inboundIngestor()->ingest($this->qrAsset(), $event);
        self::assertSame($event['message']['timestamp'], $message->getConversation()->getLastMessageAt()->getTimestamp());
        self::assertSame(0, $message->getConversation()->getUnreadCount());
        self::assertFalse($this->historyStates[$message->getConversation()->getId()]->needsResponse());
        self::assertSame([], $this->inboxCalls);
    }

    public function testHistoricalMessageIdsAreScopedToEachAccount(): void
    {
        $ingestor = $this->inboundIngestor();
        $event = $this->messageEvent('5511999999999@s.whatsapp.net', 'histórico');
        $event['message']['historical'] = true;
        $first = $ingestor->ingest($this->qrAsset('account-a'), $event);
        $second = $ingestor->ingest($this->qrAsset('account-b'), $event);
        self::assertNotSame($first->getExternalId(), $second->getExternalId());
        self::assertSame('ABC123', $first->getPayload()['message']['id']);
        self::assertCount(2, $this->persistedMessages());
    }

    public function testHistoryAndPhoneMirrorsCannotCreateGroupConversations(): void
    {
        $ingestor = $this->inboundIngestor();
        foreach ([false, true] as $fromMe) {
            $event = $this->messageEvent('123456@g.us');
            $event['message']['from_me'] = $fromMe;
            $event['message']['historical'] = true;
            self::assertNull($ingestor->ingest($this->qrAsset(), $event));
        }
        self::assertSame([], $this->persistedMessages());
        self::assertSame([], $this->inboxCalls);
    }

    public function testItCreatesTheConversationAndTheMessage(): void
    {
        $asset = $this->qrAsset();

        $message = $this->inboundIngestor()->ingest($asset, $this->messageEvent('5511999999999@s.whatsapp.net', 'bom dia'));

        self::assertNotNull($message);
        self::assertCount(1, $this->persistedMessages());
        self::assertCount(1, $this->persistedConversations());

        // O canal e `whatsapp`, e nao um canal proprio: a caixa, a janela de resposta e a
        // busca por conversa sao as mesmas dos numeros oficiais. Um canal so deste plugin
        // faria a conversa existir num lugar que nenhuma tela consulta.
        self::assertSame('whatsapp', $message->getChannel());
        self::assertSame('inbound', $message->getDirection());
        self::assertSame('text', $message->getMessageType());
        self::assertSame('received', $message->getStatus());
        self::assertSame('ABC123', $message->getExternalId());
        self::assertSame('5511999999999', $message->getRecipient());
        self::assertSame($asset, $message->getAsset());

        $conversation = $message->getConversation();
        self::assertNotNull($conversation);
        self::assertSame('5511999999999', $conversation->getRecipient());
        self::assertSame('whatsapp', $conversation->getChannel());
        self::assertSame('open', $conversation->getStatus());
        self::assertSame(1, $conversation->getUnreadCount());
        self::assertNotNull($conversation->getLastInboundAt());

        // O corpo do servico fica gravado como chegou: e o unico registro do que o
        // WhatsApp entregou, e reescreve-lo no formato do Graph apagaria a diferenca.
        self::assertSame('bom dia', $message->getPayload()['message']['text']);
    }

    public function testTheSameMessageTwiceIsStoredOnce(): void
    {
        // O dedupe da porta e por id de evento; este e por id de mensagem. Os dois
        // existem porque o servico pode reenviar o mesmo evento com id novo depois de um
        // reinicio -- e a segunda copia viraria uma segunda bolha na tela do atendente.
        $asset = $this->qrAsset();
        $ingestor = $this->inboundIngestor();
        $event = $this->messageEvent('5511999999999@s.whatsapp.net', 'bom dia');

        $ingestor->ingest($asset, $event);
        $repetida = $ingestor->ingest($asset, $event);

        self::assertNull($repetida);
        self::assertCount(1, $this->persistedMessages());
    }

    public function testItCallsMessagePersisted(): void
    {
        $asset = $this->qrAsset();

        $this->inboundIngestor()->ingest($asset, $this->messageEvent('5511999999999@s.whatsapp.net'));

        $avisos = array_values(array_filter($this->inboxCalls, static fn (array $call): bool => 'messagePersisted' === $call['call']));
        self::assertCount(1, $avisos, 'sem messagePersisted() a conversa nao entra em fila nenhuma e nao gera push');
        // A conversa ja estava ligada quando a caixa foi avisada. Avisar antes de
        // `record()` faria a caixa cair fora no primeiro if, calada.
        self::assertSame('5511999999999', $avisos[0]['conversation']);
        self::assertSame('5511999999999', $avisos[0]['recipient']);

        // A automacao so roda depois do aviso, e passa pela guarda da caixa: e ela que
        // impede a IA de responder uma conversa que um humano assumiu.
        $ordem = array_column($this->inboxCalls, 'call');
        self::assertSame(['messagePersisted', 'automationAllowed'], $ordem);
    }

    public function testALegacyBrazilianNumberGetsTheNinthDigit(): void
    {
        // O WhatsApp entrega numero antigo de celular brasileiro sem o nono digito. Duas
        // formas do mesmo telefone abririam duas conversas, e o historico do cliente
        // ficaria partido em duas metades que ninguem liga de volta.
        $asset = $this->qrAsset();

        $message = $this->inboundIngestor()->ingest($asset, $this->messageEvent('551188887777@s.whatsapp.net'));

        self::assertNotNull($message);
        self::assertSame('5511988887777', $message->getRecipient());
    }

    public function testAJidWithoutAPhoneCreatesAConversationThatCannotReply(): void
    {
        // O whatsmeow as vezes entrega um identificador de privacidade no lugar do
        // telefone. Inventar um destinatario daria uma conversa que aparece, aceita
        // resposta e falha em definitivo no envio -- porque `normalize()` lanca
        // InvalidArgumentException, que a fila le como falha permanente.
        $asset = $this->qrAsset();

        $message = $this->inboundIngestor()->ingest($asset, $this->messageEvent('220518514233310@lid', 'e esse aqui'));

        self::assertNotNull($message);
        $conversation = $message->getConversation();
        self::assertNotNull($conversation, 'a conversa e criada: a mensagem do cliente nao pode sumir');

        // O destinatario guarda o JID com o prefixo, e nao os digitos do identificador:
        // digito solto pareceria telefone para quem for enviar.
        self::assertSame(InboundIngestor::UNRESOLVED_PREFIX.'220518514233310@lid', $conversation->getRecipient());
        self::assertSame($conversation->getRecipient(), $message->getRecipient());
        self::assertTrue(InboundIngestor::isUnresolved($conversation->getRecipient()));
        self::assertNull($message->getContact());

        // A marca e o motivo ficam gravados na mensagem, por escrito, para a caixa
        // fechar o compositor dizendo por que -- e nao so deixar o botao cinza.
        $marca = $message->getPayload()['whatsqr'];
        self::assertFalse($marca['phone_resolved']);
        self::assertSame('220518514233310@lid', $marca['jid']);
        self::assertSame(InboundIngestor::UNRESOLVED_PHONE_REASON, $marca['reply_blocked_reason']);

        // E a caixa foi avisada assim mesmo: a conversa tem de aparecer na fila, com o
        // texto do cliente legivel. O que ela nao pode e aceitar resposta.
        $avisos = array_values(array_filter($this->inboxCalls, static fn (array $call): bool => 'messagePersisted' === $call['call']));
        self::assertCount(1, $avisos);
    }

    public function testMediaBecomesAnUnsupportedMessage(): void
    {
        // Midia esta fora do escopo, mas nao pode sumir: o cliente manda a foto do boleto
        // e escreve "e esse aqui". A caixa ja sabe desenhar `unsupported` com o texto
        // pedindo para reenviar; sem isso o atendente leria so o "e esse aqui".
        $asset = $this->qrAsset();

        $message = $this->inboundIngestor()->ingest($asset, $this->messageEvent('5511999999999@s.whatsapp.net', '', true));

        self::assertNotNull($message);
        self::assertSame('unsupported', $message->getMessageType());
        self::assertNotNull($message->getConversation());

        $avisos = array_values(array_filter($this->inboxCalls, static fn (array $call): bool => 'messagePersisted' === $call['call']));
        self::assertSame('unsupported', $avisos[0]['messageType']);
    }

    public function testAnOptOutKeywordIsRegisteredOnThisChannelToo(): void
    {
        // O risco central deste canal e banimento. Honrar "SAIR" no numero oficial e
        // ignora-lo no numero por QR seria o sistema anotar o pedido de saida num lugar e
        // continuar mandando pelo outro.
        $asset = $this->qrAsset();

        $message = $this->inboundIngestor()->ingest($asset, $this->messageEvent('5511999999999@s.whatsapp.net', 'SAIR'));

        self::assertNotNull($message);
        self::assertSame('opt_out', $message->getPayload()['whatsqr']['consent_keyword']);
    }

    public function testAttachmentKeepsItsTypeCaptionAndWakesTheInboxOnce(): void
    {
        $asset = $this->qrAsset();
        $event = $this->messageEvent('5511999999999@s.whatsapp.net', 'segue o relatório', false);
        $event['message']['attachment'] = ['type' => 'document', 'id' => str_repeat('c', 64), 'filename' => 'relatório.pdf', 'file_size' => 200];
        $ingestor = $this->inboundIngestor();
        $message = $ingestor->ingest($asset, $event);
        self::assertSame('document', $message?->getMessageType());
        self::assertSame('segue o relatório', $message?->getPayload()['message']['document']['caption']);
        self::assertSame(str_repeat('c', 64), $message?->getPayload()['message']['document']['id']);
        self::assertNotNull($message?->getConversation());
        self::assertNull($ingestor->ingest($asset, $event));
        $notifications = array_filter($this->inboxCalls, static fn (array $call): bool => 'messagePersisted' === $call['call']);
        self::assertCount(1, $notifications);
    }

    public function testASessionEventIsNotAMessage(): void
    {
        // Queda e reconexao sao estado do numero, nao conversa. Gravar uma mensagem por
        // evento de sessao encheria a caixa de bolhas que nenhum cliente escreveu.
        $asset = $this->qrAsset();

        $ignorado = $this->inboundIngestor()->ingest($asset, [
            'id' => 'session:deadbeef',
            'type' => 'session',
            'session_id' => 'sess-atendimento',
            'state' => 'logged_out',
            'reason' => 'sessao encerrada no aparelho',
        ]);

        self::assertNull($ignorado);
        self::assertSame([], $this->persistedMessages());
        self::assertSame([], $this->inboxCalls);
    }
}
