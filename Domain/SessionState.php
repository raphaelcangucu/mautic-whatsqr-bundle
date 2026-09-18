<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

/**
 * O estado de uma sessao como o plugin o conhece, e nao como o servico o escreveu.
 *
 * Existe para que o dialeto de um motor nao vaze para dentro: hoje o whatsmeow diz
 * "logged_out", e um motor futuro pode dizer outra coisa. Quem traduz e o adaptador;
 * tela, fila e transporte leem sempre estas cinco palavras.
 *
 * Classe com constantes, e nao enum, porque este valor tambem atravessa `settings` do
 * asset como JSON: um enum obrigaria a converter nas duas pontas toda vez, e o valor
 * gravado continuaria sendo a string mesmo assim.
 */
final readonly class SessionState
{
    public const PAIRING = 'pairing';
    public const CONNECTED = 'connected';
    public const RECONNECTING = 'reconnecting';
    public const LOGGED_OUT = 'logged_out';
    public const FAILED = 'failed';

    private const KNOWN = [self::PAIRING, self::CONNECTED, self::RECONNECTING, self::LOGGED_OUT, self::FAILED];

    public function __construct(
        public string $sessionId,
        public string $status,
        public ?string $qr = null,
        public ?string $jid = null,
        public ?string $reason = null,
    ) {
        if ('' === trim($sessionId)) {
            throw new \InvalidArgumentException('Um estado de sessao sem id nao diz de qual numero fala.');
        }
        if (!in_array($status, self::KNOWN, true)) {
            // Guarda de ultimo recurso. Quem traduz o dialeto e o adaptador, e um estado
            // desconhecido chegando aqui significa que ele deixou passar -- melhor
            // estourar do que gravar em `settings` uma palavra que ninguem mais le.
            throw new \InvalidArgumentException(sprintf('Estado de sessao desconhecido: "%s".', $status));
        }
    }

    public function isPairing(): bool
    {
        return self::PAIRING === $this->status;
    }

    public function isConnected(): bool
    {
        return self::CONNECTED === $this->status;
    }
}
