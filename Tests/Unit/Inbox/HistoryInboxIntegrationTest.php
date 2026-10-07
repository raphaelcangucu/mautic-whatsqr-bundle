<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Inbox;

use Doctrine\ORM\EntityManagerInterface;
use Doctrine\ORM\EntityRepository;
use MauticPlugin\MauticInboxBundle\Entity\ConversationState;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticWhatsQrBundle\Inbox\HistoryInboxIntegration;
use PHPUnit\Framework\TestCase;

final class HistoryInboxIntegrationTest extends TestCase
{
    public function testTimelyPhoneReplyStopsAiAndClearsWaitingWithoutChangingAssignmentOrLifecycle(): void
    {
        $conversation = (new MetaConversation())->setLastInboundAt(new \DateTimeImmutable('-10 seconds'));
        $state = (new ConversationState())->setConversation($conversation)->setLifecycle('resolved')->setNeedsResponse(true);
        $repo = $this->createMock(EntityRepository::class);
        $repo->expects(self::once())->method('findOneBy')->with(['conversation' => $conversation])->willReturn($state);
        $em = $this->createMock(EntityManagerInterface::class);
        $em->method('getRepository')->with(ConversationState::class)->willReturn($repo);
        $em->expects(self::once())->method('persist')->with($state);
        $em->expects(self::once())->method('flush');
        $message = (new MetaMessage())->setConversation($conversation)->setDirection('outbound')->setDateAdded(new \DateTimeImmutable('-5 seconds'));
        (new HistoryInboxIntegration($em))->recordDeviceReply($message);
        self::assertFalse($state->needsResponse());
        self::assertTrue($state->isHumanTakeover());
        self::assertSame('resolved', $state->getLifecycle());
        self::assertNull($state->getAssignee());
        self::assertSame(2, $state->getVersion());
    }

    public function testDelayedPhoneReplyDoesNotClearANewerCustomerQuestion(): void
    {
        $conversation = (new MetaConversation())->setLastInboundAt(new \DateTimeImmutable('-5 seconds'));
        $em = $this->createMock(EntityManagerInterface::class);
        $em->expects(self::never())->method('getRepository');
        $em->expects(self::never())->method('persist');
        $em->expects(self::never())->method('flush');
        $message = (new MetaMessage())->setConversation($conversation)->setDirection('outbound')->setDateAdded(new \DateTimeImmutable('-10 seconds'));
        (new HistoryInboxIntegration($em))->recordDeviceReply($message);
    }
}
