<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Support;

use Doctrine\ORM\EntityManagerInterface;
use Mautic\CampaignBundle\Executioner\RealTimeExecutioner;
use Mautic\LeadBundle\Model\LeadModel;
use Mautic\LeadBundle\Tracker\ContactTracker;
use MauticPlugin\MauticMetaBundle\Application\Adapter\WebhookAdapterDispatcher;
use MauticPlugin\MauticMetaBundle\Application\Automation\CampaignMessageDispatcher;
use MauticPlugin\MauticMetaBundle\Application\Contact\ContactMatcher;
use MauticPlugin\MauticMetaBundle\Application\Contact\IdentityManager;
use MauticPlugin\MauticMetaBundle\Application\Conversation\ConversationManager;
use MauticPlugin\MauticMetaBundle\Application\Support\InboxIntegrationInterface;
use MauticPlugin\MauticMetaBundle\Application\WhatsApp\ConsentKeywordMatcher;
use MauticPlugin\MauticMetaBundle\Application\WhatsApp\PhoneNormalizer;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAdapterDeliveryRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaContactIdentity;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversationRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaConnection;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessageRepository;
use MauticPlugin\MauticWhatsQrBundle\Application\InboundIngestor;
use Psr\Log\NullLogger;

/**
 * O ingestor de verdade sobre um banco de memoria.
 *
 * Existe como trait porque duas suites precisam do mesmo objeto montado: a do ingestor,
 * que afirma o que ele grava, e a do webhook, que so precisa que a costura exista e nao
 * estoure. Um duble de ingestor na suite do webhook concordaria com qualquer coisa -- e
 * a costura entre a porta e a gravacao e justamente o que a tarefa 5 acrescenta.
 *
 * Quase tudo aqui e o objeto real do Meta bundle, e nao um duble, porque as classes que
 * importam sao `final`: montar o ConversationManager de verdade sobre um repositorio de
 * memoria afirma que a conversa e a mensagem ficam ligadas como na producao, e nao que
 * um mock devolveu o que o teste mandou devolver.
 */
trait InboundIngestorFixture
{
    /**
     * Tudo que passou por persist(), na ordem.
     *
     * @var list<object>
     */
    private array $persisted = [];

    /**
     * As conversas "no banco", por asset+canal+destinatario.
     *
     * @var array<string, MetaConversation>
     */
    private array $conversationRows = [];

    /**
     * As mensagens "no banco", por asset+externalId -- e por onde o dedupe pergunta.
     *
     * @var array<string, MetaMessage>
     */
    private array $messageRows = [];

    /**
     * O que a caixa recebeu, na ordem em que recebeu. Cada item guarda o destinatario da
     * conversa no momento da chamada: e assim que se afirma que `record()` veio antes.
     *
     * @var list<array<string, mixed>>
     */
    private array $inboxCalls = [];

    private int $nextRowId = 1;

    private function qrAsset(string $sessionId = 'sess-atendimento', array $settings = []): MetaAsset
    {
        return (new MetaAsset())
            ->setType(AssetType::WhatsAppQrSession)
            ->setExternalId($sessionId)
            ->setName('Numero '.$sessionId)
            ->setStatus('active')
            ->setSettings($settings)
            ->setConnection(new MetaConnection(1));
    }

    private function inboundIngestor(): InboundIngestor
    {
        $entityManager = $this->memoryEntityManager();
        $phones = new PhoneNormalizer();
        $inbox = $this->inboxSpy();

        return new InboundIngestor(
            $entityManager,
            $this->messageRepository(),
            $this->identityManager(),
            new ContactMatcher($this->createMock(LeadModel::class), $entityManager),
            new ConsentKeywordMatcher(),
            $phones,
            new ConversationManager($this->conversationRepository(), $entityManager, $phones, $inbox),
            $inbox,
            new CampaignMessageDispatcher(
                $this->createMock(ContactTracker::class),
                $this->createMock(RealTimeExecutioner::class),
                new NullLogger(),
            ),
            new WebhookAdapterDispatcher($entityManager, $this->createMock(MetaAdapterDeliveryRepository::class)),
            new NullLogger(),
        );
    }

    private function inboxSpy(): InboxIntegrationInterface
    {
        $inbox = $this->createMock(InboxIntegrationInterface::class);
        $inbox->method('ownsSupportInbox')->willReturn(true);
        $inbox->method('automationAllowed')->willReturnCallback(function (MetaAsset $asset, string $recipient): bool {
            $this->inboxCalls[] = ['call' => 'automationAllowed', 'recipient' => $recipient];

            return true;
        });
        $inbox->method('messagePersisted')->willReturnCallback(function (MetaMessage $message): void {
            $this->inboxCalls[] = [
                'call' => 'messagePersisted',
                'recipient' => $message->getRecipient(),
                // Sem conversa a caixa desiste no primeiro if: e isto que prova que a
                // gravacao veio antes do aviso, e nao o contrario.
                'conversation' => $message->getConversation()?->getRecipient(),
                'messageType' => $message->getMessageType(),
            ];
        });

        return $inbox;
    }

    private function memoryEntityManager(): EntityManagerInterface
    {
        $entityManager = $this->createMock(EntityManagerInterface::class);
        $entityManager->method('isOpen')->willReturn(true);
        $entityManager->method('persist')->willReturnCallback(function (object $entity): void {
            $this->persisted[] = $entity;
            if ($entity instanceof MetaConversation) {
                $this->assignRowId($entity, MetaConversation::class);
                $this->conversationRows[$this->conversationKey($entity->getAsset(), $entity->getChannel(), $entity->getRecipient())] = $entity;
            }
            if ($entity instanceof MetaMessage) {
                // O id vem do banco na vida real, e a caixa grava esse id no estado da
                // conversa. Sem ele aqui, o teste afirmaria menos do que a producao faz.
                $this->assignRowId($entity, MetaMessage::class);
                $this->messageRows[$entity->getAsset()->getExternalId().'|'.$entity->getExternalId()] = $entity;
            }
        });

        return $entityManager;
    }

    private function conversationRepository(): MetaConversationRepository
    {
        $repository = $this->createMock(MetaConversationRepository::class);
        $repository->method('findOneBy')->willReturnCallback(function (array $criteria): ?MetaConversation {
            $asset = $criteria['asset'] ?? null;
            if (!$asset instanceof MetaAsset) {
                return null;
            }

            return $this->conversationRows[$this->conversationKey($asset, (string) ($criteria['channel'] ?? ''), (string) ($criteria['recipient'] ?? ''))] ?? null;
        });

        return $repository;
    }

    private function messageRepository(): MetaMessageRepository
    {
        $repository = $this->createMock(MetaMessageRepository::class);
        $repository->method('findOneBy')->willReturnCallback(function (array $criteria): ?MetaMessage {
            $asset = $criteria['asset'] ?? null;
            if (!$asset instanceof MetaAsset) {
                return null;
            }

            return $this->messageRows[$asset->getExternalId().'|'.(string) ($criteria['externalId'] ?? '')] ?? null;
        });

        return $repository;
    }

    private function identityManager(): IdentityManager
    {
        $identities = $this->createMock(IdentityManager::class);
        $identities->method('registerInteraction')->willReturnCallback(
            static fn (MetaAsset $asset, string $externalId, ?string $username = null, $contact = null): MetaContactIdentity => (new MetaContactIdentity())
                ->setAsset($asset)
                ->setExternalId($externalId)
                ->setContact($contact)
        );

        return $identities;
    }

    private function conversationKey(MetaAsset $asset, string $channel, string $recipient): string
    {
        return $asset->getExternalId().'|'.$channel.'|'.$recipient;
    }

    private function assignRowId(object $entity, string $class): void
    {
        $id = new \ReflectionProperty($class, 'id');
        if (null === $id->getValue($entity)) {
            $id->setValue($entity, $this->nextRowId++);
        }
    }

    /**
     * @return list<MetaMessage>
     */
    private function persistedMessages(): array
    {
        return $this->distinctPersisted(MetaMessage::class);
    }

    /**
     * @return list<MetaConversation>
     */
    private function persistedConversations(): array
    {
        return $this->distinctPersisted(MetaConversation::class);
    }

    /**
     * As entidades distintas que passaram por persist(), e nao quantas vezes passaram.
     *
     * A mesma mensagem e persistida mais de uma vez na vida real -- quem grava e quem
     * liga a conversa chamam persist() no mesmo objeto, que e o que o Doctrine espera.
     * Contar chamadas em vez de objetos afirmaria um numero que nao diz nada sobre
     * quantas bolhas o atendente ve.
     *
     * @return list<object>
     */
    private function distinctPersisted(string $class): array
    {
        $distinct = [];
        foreach ($this->persisted as $entity) {
            if ($entity instanceof $class) {
                $distinct[spl_object_id($entity)] = $entity;
            }
        }

        return array_values($distinct);
    }

    /**
     * O corpo que o servico em Go manda -- ver service/webhook/sender.go, tipo `payload`.
     */
    private function messageEvent(string $from, string $text = 'bom dia', bool $unsupported = false, string $externalId = 'ABC123'): array
    {
        return [
            'id' => 'msg:'.$externalId,
            'type' => 'message',
            'session_id' => 'sess-atendimento',
            'state' => 'connected',
            'jid' => '5511333333333@s.whatsapp.net',
            'object' => 'whatsapp_qr_session',
            'message' => [
                'id' => $externalId,
                'from' => $from,
                'text' => $text,
                'timestamp' => time(),
                'unsupported' => $unsupported,
            ],
        ];
    }
}
