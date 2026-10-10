<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use Doctrine\ORM\EntityManagerInterface;
use MauticPlugin\MauticMetaBundle\Application\WhatsApp\PhoneNormalizer;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversationRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticWhatsQrBundle\Domain\HistoryInboxIntegrationInterface;
use MauticPlugin\MauticWhatsQrBundle\Domain\InboundJid;

/** One acknowledged history event per transaction, with no notifications or bots. */
final class HistoryRecorder
{
    public function __construct(
        private readonly MetaConversationRepository $conversations,
        private readonly EntityManagerInterface $entityManager,
        private readonly PhoneNormalizer $phones,
        private readonly ?HistoryInboxIntegrationInterface $inbox = null,
    ) {}

    public function record(MetaMessage $message): void
    {
        $this->entityManager->getConnection()->transactional(function () use ($message): void {
            $recipient = $message->getRecipient();
            $equivalents = InboundJid::isUnresolved($recipient) ? [$recipient] : $this->phones->equivalentRecipients($recipient, (string) ($message->getAsset()->getSettings()['default_region'] ?? 'BR'));
            // Both Brazilian number forms in one lookup, never queries inside a loop.
            $conversation = $this->conversations->findOneBy(['asset' => $message->getAsset(), 'channel' => 'whatsapp', 'recipient' => $equivalents]);
            $date = $message->getDateAdded();
            if (!$conversation instanceof MetaConversation) {
                $conversation = (new MetaConversation())->setAsset($message->getAsset())->setChannel('whatsapp')->setRecipient($recipient)->setLastMessageAt($date);
            } elseif ($date > $conversation->getLastMessageAt()) {
                $conversation->setLastMessageAt($date);
            }
            if ('inbound' === $message->getDirection() && (null === $conversation->getLastInboundAt() || $date > $conversation->getLastInboundAt())) {
                $conversation->setLastInboundAt($date);
            }
            // Preserve existing assignment, lifecycle and unread count.
            $message->setConversation($conversation)->setContact($conversation->getContact());
            $this->entityManager->persist($conversation);
            $this->entityManager->persist($message);
            $this->entityManager->flush();
            $this->inbox?->record($message);
        });
    }

    public function refreshContent(MetaMessage $message): void
    {
        // Preserve the existing Inbox state and wake incremental SSE only.
        $this->inbox?->record($message);
    }

    public function recordDeviceReply(MetaMessage $message): void
    {
        $this->inbox?->recordDeviceReply($message);
    }
}
