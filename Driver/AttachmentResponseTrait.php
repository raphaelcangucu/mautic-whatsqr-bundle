<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\AttachmentReference;
use Symfony\Component\HttpFoundation\HeaderUtils;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;
use Symfony\Component\HttpFoundation\StreamedResponse;

trait AttachmentResponseTrait
{
    public function attachmentResponse(MetaAsset $asset, AttachmentReference $attachment, Request $request): Response
    {
        $privateHeaders = ['Cache-Control' => 'private, no-store', 'X-Content-Type-Options' => 'nosniff', 'Content-Security-Policy' => "sandbox; default-src 'none'"];
        if (1 !== preg_match('/^[a-f0-9]{64}$/D', $attachment->id) || $attachment->size <= 0 || $attachment->size > AttachmentReference::MAX_BYTES || null !== $attachment->error || !in_array($attachment->type, AttachmentReference::TYPES, true)) { return new Response('', 404, $privateHeaders); }
        $headers = [];
        $range = $request->headers->get('Range');
        if (null !== $range) {
            if (1 !== preg_match('/^bytes=(?:\d{1,12}-\d{0,12}|-\d{1,12})$/D', $range)) { return new Response('', 416, $privateHeaders); }
            $headers['Range'] = $range;
        }
        $etag = $request->headers->get('If-None-Match');
        if (is_string($etag) && strlen($etag) < 128 && !str_contains($etag, "\r") && !str_contains($etag, "\n")) { $headers['If-None-Match'] = $etag; }
        $upstream = null;
        try {
        $upstream = $this->http->request('GET', $this->baseUri.'/sessions/'.rawurlencode($this->sessionId($asset)).'/media/'.$attachment->id, [
            'auth_bearer' => $this->token, 'headers' => $headers,
            'max_redirects' => 0, 'buffer' => false, 'timeout' => 28, 'max_duration' => 32,
        ]);
            $status = $upstream->getStatusCode();
            $source = $upstream->getHeaders(false);
            if (304 === $status) { $upstream->cancel(); return new Response('', 304, array_replace($privateHeaders, ['Cache-Control' => 'private, max-age=300', 'ETag' => '"'.$attachment->id.'"'])); }
            if (416 === $status) {
                $upstream->cancel();
                $contentRange = $source['content-range'][0] ?? '';
                return new Response('', 416, array_merge($privateHeaders, 'bytes */'.$attachment->size === $contentRange ? ['Content-Range' => $contentRange] : []));
            }
            if (!in_array($status, [200, 206], true)) { $upstream->cancel(); return new Response('', 404, $privateHeaders); }
            $length = $source['content-length'][0] ?? '';
            if (1 !== preg_match('/^[0-9]{1,10}$/D', $length) || (int) $length <= 0 || (int) $length > $attachment->size || (200 === $status && (int) $length !== $attachment->size)) { throw new \RuntimeException('Invalid attachment length.'); }
            $mime = strtolower(trim(explode(';', $source['content-type'][0] ?? '')[0]));
            $allowed = match ($attachment->type) {
                'image', 'sticker' => ['image/jpeg', 'image/png', 'image/gif', 'image/webp'],
                'video' => ['video/mp4', 'video/webm', 'video/3gpp'],
                'audio' => ['audio/aac', 'audio/amr', 'audio/ogg', 'audio/mpeg', 'audio/mp4', 'audio/wave', 'audio/webm'],
                'document' => ['application/pdf', 'text/plain', 'text/csv', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 'application/vnd.openxmlformats-officedocument.presentationml.presentation'],
                default => [],
            };
            if (!in_array($mime, $allowed, true)) { throw new \RuntimeException('Invalid attachment content type.'); }
            $inline = 'document' !== $attachment->type;
            $name = '' !== $attachment->filename ? $attachment->filename : $attachment->type;
            $name = mb_substr(preg_replace('/[\p{Cc}\p{Cf}]/u', '', basename(str_replace('\\', '/', $name))) ?? 'attachment', 0, 160);
            $responseHeaders = [
                'Content-Type' => $mime, 'Content-Length' => $length, 'Accept-Ranges' => 'bytes',
                'Content-Disposition' => HeaderUtils::makeDisposition($inline ? HeaderUtils::DISPOSITION_INLINE : HeaderUtils::DISPOSITION_ATTACHMENT, $name, 'attachment'),
                'Cache-Control' => 'private, max-age=300', 'ETag' => '"'.$attachment->id.'"',
                'X-Content-Type-Options' => 'nosniff', 'Content-Security-Policy' => "sandbox; default-src 'none'",
            ];
            if (206 === $status) {
                $contentRange = $source['content-range'][0] ?? '';
                if (null === $range || 1 !== preg_match('/^bytes (\d{1,10})-(\d{1,10})\/(\d{1,10})$/D', $contentRange, $parts) || (int) $parts[3] !== $attachment->size || (int) $parts[1] > (int) $parts[2] || (int) $parts[2] >= $attachment->size || (int) $parts[2] - (int) $parts[1] + 1 !== (int) $length) { throw new \RuntimeException('Invalid media range.'); }
                $responseHeaders['Content-Range'] = $contentRange;
            }
            return new StreamedResponse(function () use ($upstream, $length): void {
                $received = 0;
                try {
                    foreach ($this->http->stream($upstream, 28) as $chunk) {
                        if ($chunk->isTimeout()) { break; }
                        $data = $chunk->getContent(); $received += strlen($data);
                        if ($received > (int) $length || connection_aborted()) { break; }
                        echo $data;
                    }
                } catch (\Throwable) { /* Fail closed after a broken upstream stream. */ }
                finally { $upstream->cancel(); }
            }, $status, $responseHeaders);
        } catch (\Throwable) {
            $upstream?->cancel();
            return new Response('', 404, $privateHeaders);
        }
    }
}
