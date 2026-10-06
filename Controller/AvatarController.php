<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Controller;

use Mautic\CoreBundle\Controller\CommonController;
use Mautic\CoreBundle\Security\Permissions\CorePermissions;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversationRepository;
use MauticPlugin\MauticWhatsQrBundle\Application\ConversationAvatar;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

final class AvatarController extends CommonController
{
    public function show(int $conversationId, Request $request, CorePermissions $permissions, MetaConversationRepository $conversations, ConversationAvatar $avatars): Response
    {
        if (!$permissions->isGranted(['inbox:conversations:view', 'meta:messages:view'])) {
            throw $this->createAccessDeniedException();
        }
        $conversation = $conversations->createQueryBuilder('c')->addSelect('a')->join('c.asset', 'a')
            ->andWhere('c.id = :id')->setParameter('id', $conversationId)->getQuery()->getOneOrNullResult();
        if (!$conversation instanceof MetaConversation || AssetType::WhatsAppQrSession !== $conversation->getAsset()->getType()) {
            return new Response('', 404, ['Cache-Control' => 'private, no-store']);
        }

        if ($request->hasSession() && $request->getSession()->isStarted()) {
            $request->getSession()->save();
        }

        return $avatars->response($request, $conversation);
    }
}
