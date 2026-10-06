<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Controller;

use Mautic\CoreBundle\Controller\CommonController;
use Mautic\CoreBundle\Security\Permissions\CorePermissions;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessageRepository;
use MauticPlugin\MauticWhatsQrBundle\Domain\AttachmentReference;
use MauticPlugin\MauticWhatsQrBundle\Driver\AttachmentDriverInterface;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

final class MediaController extends CommonController
{
    public function show(int $messageId, Request $request, CorePermissions $permissions, MetaMessageRepository $messages, SessionDriverFactory $drivers): Response
    {
        if (!$permissions->isGranted(['inbox:conversations:view', 'meta:messages:view'])) { throw $this->createAccessDeniedException(); }
        $message = $messages->createQueryBuilder('m')->addSelect('a')->join('m.asset', 'a')
            ->where('m.id = :id')->setParameter('id', $messageId)->getQuery()->getOneOrNullResult();
        $privateHeaders = ['Cache-Control' => 'private, no-store', 'X-Content-Type-Options' => 'nosniff'];
        if (!$message instanceof MetaMessage || 'inbound' !== $message->getDirection() || AssetType::WhatsAppQrSession !== $message->getAsset()->getType()) { return new Response('', 404, $privateHeaders); }
        $reference = AttachmentReference::fromInbound($message->getPayload()['message'] ?? []);
        if (null === $reference || '' === $reference->id || $reference->type !== $message->getMessageType()) { return new Response('', 404, $privateHeaders); }
        if ($request->hasSession() && $request->getSession()->isStarted()) { $request->getSession()->save(); }
        try {
            $driver = $drivers->forAsset($message->getAsset());
            return $driver instanceof AttachmentDriverInterface ? $driver->attachmentResponse($message->getAsset(), $reference, $request) : new Response('', 404, $privateHeaders);
        } catch (\Throwable) { return new Response('', 404, $privateHeaders); }
    }
}
