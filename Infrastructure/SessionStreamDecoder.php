<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Infrastructure;

/** SSE frames can arrive split at any byte, including within UTF-8 or CRLF. */
final class SessionStreamDecoder
{
    private string $buffer = '';

    /** @return list<array{event: string, data: string}> */
    public function push(string $chunk): array
    {
        $this->buffer .= $chunk;
        if (strlen($this->buffer) > 131072) {
            throw new \RuntimeException('Evento de conexão excedeu o limite de tamanho.');
        }
        $frames = [];
        while (preg_match('/\r?\n\r?\n/', $this->buffer, $separator, PREG_OFFSET_CAPTURE)) {
            $offset = $separator[0][1];
            $frame = substr($this->buffer, 0, $offset);
            $this->buffer = substr($this->buffer, $offset + strlen($separator[0][0]));
            $event = 'message';
            $data = [];
            foreach (preg_split('/\r?\n/', $frame) as $line) {
                if (str_starts_with($line, 'event:')) { $event = trim(substr($line, 6)); }
                if (str_starts_with($line, 'data:')) { $data[] = ltrim(substr($line, 5), ' '); }
            }
            $frames[] = ['event' => $event, 'data' => implode("\n", $data)];
        }
        return $frames;
    }
}
