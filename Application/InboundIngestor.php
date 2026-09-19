<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use Doctrine\ORM\EntityManagerInterface;
use MauticPlugin\MauticMetaBundle\Application\Adapter\WebhookAdapterDispatcher;
use MauticPlugin\MauticMetaBundle\Application\Automation\CampaignMessageDispatcher;
use MauticPlugin\MauticMetaBundle\Application\Contact\ContactMatcher;
use MauticPlugin\MauticMetaBundle\Application\Contact\IdentityManager;
use MauticPlugin\MauticMetaBundle\Application\Conversation\ConversationManager;
use MauticPlugin\MauticMetaBundle\Application\Support\InboxIntegrationInterface;
use MauticPlugin\MauticMetaBundle\Application\WhatsApp\ConsentKeywordMatcher;
use MauticPlugin\MauticMetaBundle\Application\WhatsApp\PhoneNormalizer;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessageRepository;
use MauticPlugin\MauticWhatsQrBundle\Domain\InboundJid;
use MauticPlugin\MauticWhatsQrBundle\Domain\WebhookEventType;
use Psr\Log\LoggerInterface;

/**
 * O evento conferido virando conversa, mensagem -- e caixa acordada.
 *
 * A ultima parte e a que a primeira versao do desenho errou, e por isso vem escrita:
 * **gravar as entidades nao avisa ninguem.** Nao existe listener do Doctrine. Quem cria
 * o estado da conversa, marca que ela precisa de resposta, grava o log do evento e
 * enfileira o push e `InboxIntegrationInterface::messagePersisted()`, chamada de
 * proposito, aqui. Sem ela a conversa nao entra em fila nenhuma: a caixa fica em
 * silencio com mensagem de cliente dentro.
 *
 * A sequencia e a mesma do `WhatsAppWebhookProcessor` do Meta bundle -- gravar, chamar
 * `record()`, avisar a caixa, so entao a automacao. Nao e imitacao: e o mesmo contrato,
 * e um canal que o cumprisse pela metade apareceria pela metade na mesma tela.
 */
final class InboundIngestor
{
    /**
     * O prefixo do destinatario sem telefone resolvido, repetido aqui porque quem le o
     * ingestor nao deveria precisar abrir o Domain para saber que este caso existe.
     */
    public const UNRESOLVED_PREFIX = InboundJid::UNRESOLVED_PREFIX;

    /**
     * O motivo, por escrito, para a caixa fechar o compositor dizendo por que.
     *
     * Botao cinza sem motivo faz o atendente tentar de novo, achar que e defeito e abrir
     * chamado. O texto vai gravado na mensagem porque e ali que a caixa o encontra sem
     * precisar conhecer este plugin.
     */
    public const UNRESOLVED_PHONE_REASON = 'O WhatsApp entregou esta conversa com um identificador de privacidade no lugar do telefone. Sem o número não há para onde enviar a resposta; peça o contato por outro caminho.';

    public function __construct(
        private readonly EntityManagerInterface $entityManager,
        private readonly MetaMessageRepository $messages,
        private readonly IdentityManager $identities,
        private readonly ContactMatcher $contacts,
        private readonly ConsentKeywordMatcher $keywords,
        private readonly PhoneNormalizer $phones,
        private readonly ConversationManager $conversations,
        private readonly InboxIntegrationInterface $inbox,
        private readonly CampaignMessageDispatcher $campaigns,
        private readonly WebhookAdapterDispatcher $adapters,
        private readonly SessionStateRecorder $sessions,
        private readonly LoggerInterface $logger,
    ) {
    }

    public static function isUnresolved(string $recipient): bool
    {
        return InboundJid::isUnresolved($recipient);
    }

    /**
     * Grava o que chegou e acorda a caixa. Devolve null quando nao ha nada a gravar.
     *
     * O asset chega pronto do controlador: quem prova de qual numero e o corpo e a
     * assinatura, e reabrir essa pergunta aqui seria decidir duas vezes com metade da
     * informacao.
     *
     * @param array<string, mixed> $payload o corpo do servico, ja conferido
     */
    public function ingest(MetaAsset $asset, array $payload): ?MetaMessage
    {
        $type = trim((string) ($payload['type'] ?? ''));

        if (WebhookEventType::SESSION === $type) {
            // Queda de sessao e estado do numero, nao conversa: nao ha bolha a gravar, e
            // gravar uma encheria a caixa do que ninguem escreveu. Mas "nao ha bolha" nao
            // e "nao ha nada a fazer", e essa confusao foi o que este ramo fez ate aqui --
            // enquanto ele devolvia nulo, nada gravava o estado do numero em lugar nenhum,
            // e a varredura das duas horas rodava todo minuto sem nunca achar numero
            // caido. Continua devolvendo null, porque o retorno e a mensagem gravada.
            $this->sessions->record($asset, $payload);

            return null;
        }

        if (WebhookEventType::MESSAGE !== $type) {
            // Status de entrega so tem o que atualizar depois que existir mensagem de
            // saida (tarefa 6).
            $this->logger->debug('whatsqr: evento sem conversa a gravar', ['type' => $payload['type'] ?? null]);

            return null;
        }

        $inbound = is_array($payload['message'] ?? null) ? $payload['message'] : [];
        $externalId = trim((string) ($inbound['id'] ?? ''));
        if ('' === $externalId) {
            // Sem id nao ha como saber se esta mensagem ja entrou. Gravar assim mesmo
            // troca "perdi uma" por "gravei duas", que e pior: a segunda bolha some do
            // aparelho do cliente e fica so na tela do atendente.
            $this->logger->warning('whatsqr: mensagem sem id descartada', ['asset' => $asset->getExternalId()]);

            return null;
        }

        // Dedupe por numero, e nao global: dois numeros podem receber o mesmo id de um
        // encaminhamento, e a segunda conversa e de outro cliente.
        if ($this->messages->findOneBy(['asset' => $asset, 'externalId' => $externalId]) instanceof MetaMessage) {
            return null;
        }

        try {
            $jid = new InboundJid((string) ($inbound['from'] ?? ''));
        } catch (\InvalidArgumentException $exception) {
            $this->logger->warning('whatsqr: mensagem sem remetente descartada -- '.$exception->getMessage(), ['asset' => $asset->getExternalId()]);

            return null;
        }

        $recipient = $this->recipient($asset, $jid);
        $resolved = !InboundJid::isUnresolved($recipient);

        // Sem telefone nao ha o que casar com contato: a busca do Meta bundle compara
        // digitos com `mobile` e `phone`, e os digitos de um identificador de privacidade
        // casariam com quem tivesse a mesma sequencia por acaso.
        $contact = $resolved ? $this->contacts->match($asset, $recipient) : null;
        $identity = $this->identities->registerInteraction($asset, $recipient, null, $contact);

        $unsupported = true === ($inbound['unsupported'] ?? false);
        $text = (string) ($inbound['text'] ?? '');
        $keyword = $unsupported ? null : $this->keywords->match($text);
        if ('opt_in' === $keyword) {
            $this->identities->optIn($identity, 'whatsapp_keyword');
        }
        if ('opt_out' === $keyword) {
            // O risco central deste canal e banimento. Honrar "SAIR" no numero oficial e
            // ignora-lo aqui seria anotar o pedido de saida num lugar e continuar mandando
            // pelo outro -- exatamente a denuncia que faz um chip cair.
            $this->identities->optOut($identity, 'whatsapp_keyword');
        }

        $payload['whatsqr'] = [
            'jid' => $jid->raw,
            'phone_resolved' => $resolved,
            'reply_blocked_reason' => $resolved ? null : self::UNRESOLVED_PHONE_REASON,
            'consent_keyword' => $keyword,
        ];

        $message = (new MetaMessage())
            ->setAsset($asset)
            ->setExternalId($externalId)
            // `whatsapp`, e nao um canal proprio: a caixa, a busca por conversa e a janela
            // de resposta sao as mesmas dos numeros oficiais. Canal so deste plugin faria
            // a conversa existir num lugar que nenhuma tela consulta.
            ->setChannel('whatsapp')
            ->setDirection('inbound')
            // Midia esta fora do escopo e nao pode sumir: a caixa ja desenha `unsupported`
            // com o texto pedindo para reenviar. Baixar o arquivo passaria pelo Graph, e
            // credencial de Graph e o que este canal justamente nao tem.
            ->setMessageType($unsupported ? 'unsupported' : 'text')
            ->setContact($identity->getContact())
            ->setRecipient($recipient)
            // O corpo fica como chegou: e o unico registro do que o WhatsApp entregou.
            ->setPayload($payload)
            ->setStatus('received');

        $this->entityManager->persist($message);
        $this->entityManager->flush();

        $conversation = $this->conversations->record($message);
        if (!$resolved) {
            $this->keepUnresolvedRecipient($conversation, $recipient);
        }

        // O aviso, que e o ponto desta tarefa. Depois de `record()`, nunca antes: sem
        // conversa ligada a caixa desiste no primeiro if, calada.
        $this->inbox->messagePersisted($message);

        // A guarda da automacao e da caixa: e ela que impede a IA de responder por cima de
        // um atendente que assumiu a conversa.
        if ($this->inbox->automationAllowed($asset, $recipient)) {
            $this->campaigns->dispatch($message);
        }

        // Os adaptadores de webhook do cliente recebem entrada de whatsapp por conexao. Um
        // canal de fora dessa lista seria uma integracao que funciona para quatro numeros e
        // emudece no quinto, sem ninguem mexer nela.
        $this->adapters->dispatch($message, 'message.received');

        return $message;
    }

    /**
     * O destinatario da conversa: o telefone, ou a marca de que nao ha telefone.
     */
    private function recipient(MetaAsset $asset, InboundJid $jid): string
    {
        if (!$jid->carriesPhone()) {
            return $jid->unresolvedRecipient();
        }

        try {
            // O mesmo normalizador dos canais oficiais, e por isso o nono digito
            // brasileiro ja vem tratado: duas formas do mesmo celular abririam duas
            // conversas, e o historico do cliente ficaria partido em duas metades.
            return $this->phones->normalizeImported($jid->user, (string) ($asset->getSettings()['default_region'] ?? 'BR'));
        } catch (\InvalidArgumentException $exception) {
            // O JID prometia telefone e o numero nao existe. Guardar os digitos assim
            // mesmo daria uma conversa que aceita resposta e falha em definitivo no envio.
            $this->logger->warning('whatsqr: JID com telefone que nao normaliza -- '.$exception->getMessage(), ['jid' => $jid->raw]);

            return $jid->unresolvedRecipient();
        }
    }

    /**
     * Devolve a marca que o Meta bundle acabou de apagar.
     *
     * `ConversationManager::record()` canoniza todo destinatario de whatsapp como
     * telefone: tira o que nao e digito e grava o que sobrou. Num identificador de
     * privacidade o que sobra sao digitos que PARECEM telefone, e a conversa passaria a
     * anunciar um numero para discar que nao chega em ninguem.
     *
     * A correcao e aqui, e nao la, porque la e outro repositorio: o lugar certo seria o
     * proprio `record()` nao canonizar destinatario marcado como sem telefone.
     */
    private function keepUnresolvedRecipient(MetaConversation $conversation, string $recipient): void
    {
        if ($conversation->getRecipient() === $recipient) {
            return;
        }

        $conversation->setRecipient($recipient);
        $this->entityManager->persist($conversation);
        $this->entityManager->flush();
    }
}
