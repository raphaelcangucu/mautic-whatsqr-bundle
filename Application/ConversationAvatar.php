<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use MauticPlugin\MauticWhatsQrBundle\Domain\ProfileImage;
use MauticPlugin\MauticWhatsQrBundle\Driver\ProfileImageDriverInterface;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

final class ConversationAvatar
{
    public function __construct(private SessionDriverFactory $drivers) {}

    public function response(Request $request, MetaConversation $conversation): Response
    {
        try {
            $driver = $this->drivers->forAsset($conversation->getAsset());
            $recipient = $conversation->getRecipient();
            if (str_starts_with($recipient, 'jid:')) { $recipient = substr($recipient, 4); }
            $photo = $driver instanceof ProfileImageDriverInterface ? $driver->profileImage($conversation->getAsset(), $recipient) : null;
        } catch (\Throwable) {
            $photo = null;
        }

        return self::imageResponse($request, $photo);
    }

    public static function imageResponse(Request $request, ?ProfileImage $photo): Response
    {
        if (null === $photo) {
            return new Response('', 404, ['Cache-Control' => 'private, max-age=300', 'X-Content-Type-Options' => 'nosniff']);
        }
        $response = new Response($photo->contents, 200, [
            'Content-Type' => $photo->mimeType,
            'Content-Length' => (string) strlen($photo->contents),
            'Content-Disposition' => 'inline',
            'Cache-Control' => 'private, max-age=3600',
            'X-Content-Type-Options' => 'nosniff',
        ]);
        $response->setEtag(hash('sha256', $photo->contents));
        $response->isNotModified($request);

        return $response;
    }
}
