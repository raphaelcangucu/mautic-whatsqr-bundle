<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Inbox;

use Doctrine\ORM\EntityManagerInterface;
use Mautic\UserBundle\Entity\User;
use MauticPlugin\MauticInboxBundle\Entity\ConversationState;
use MauticPlugin\MauticInboxBundle\Security\ConversationAccess;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticWhatsQrBundle\Domain\HistoryInboxIntegrationInterface;

final class HistoryInboxIntegration implements HistoryInboxIntegrationInterface
{
    public function __construct(
        private readonly EntityManagerInterface $entityManager,
        private readonly ?ConversationAccess $access = null,
    ) {}

    public function record(MetaMessage $message): void
    {
        $conversation = $message->getConversation();
        if (null === $conversation) { return; }
        $state = $this->entityManager->getRepository(ConversationState::class)->findOneBy(['conversation' => $conversation]);
        if (!$state instanceof ConversationState) {
            $state = (new ConversationState())->setConversation($conversation)->setNeedsResponse(false);
            $operatorId = (int) ($conversation->getAsset()->getSettings()['inbox_default_assignee_id'] ?? 0);
            if ($operatorId > 0 && null !== $this->access) {
                $operator = $this->entityManager->find(User::class, $operatorId);
                if ($operator instanceof User && $this->access->canViewInbox($operator)) { $state->setAssignee($operator); }
            }
        }
        $state->setNeedsResponse($state->needsResponse())->setVersion($state->getVersion() + 1);
        $this->entityManager->persist($state);
        $this->entityManager->flush();
        // SSE sees persisted messages and the version; no push or message.received.
    }

    public function recordDeviceReply(MetaMessage $message): void
    {
        $conversation = $message->getConversation();
        if (null === $conversation || 'outbound' !== $message->getDirection()) { return; }
        // A delayed mirror must not clear a newer unanswered customer message.
        if (($conversation->getLastInboundAt()?->getTimestamp() ?? 0) > $message->getDateAdded()->getTimestamp()) { return; }
        $state = $this->entityManager->getRepository(ConversationState::class)->findOneBy(['conversation' => $conversation]);
        if (!$state instanceof ConversationState) {
            $state = (new ConversationState())->setConversation($conversation);
        }
        $state->setNeedsResponse(false)->setHumanTakeover(true)->setVersion($state->getVersion() + 1);
        $this->entityManager->persist($state);
        $this->entityManager->flush();
    }
}
