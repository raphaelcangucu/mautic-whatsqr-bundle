<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

/**
 * Quem escreveu, como o whatsmeow o identifica -- e a pergunta que interessa: da para
 * responder para ele?
 *
 * O JID normal traz o telefone (`5511999999999@s.whatsapp.net`), mas as versoes recentes
 * do WhatsApp as vezes entregam um identificador de privacidade no lugar
 * (`220518514233310@lid`). Os digitos dele parecem telefone e nao sao: discar aquilo nao
 * chega em ninguem. Por isso a pergunta mora aqui, num objeto, e nao num `str_contains`
 * espalhado por quem precisa dela -- um lugar que esquecesse o caso opaco inventaria um
 * destinatario, e quem paga por isso e o cliente que nunca recebe a resposta.
 *
 * Objeto de valor: fica fora do contêiner, como o resto de Domain.
 */
final readonly class InboundJid
{
    /**
     * O que marca um destinatario que nao e telefone.
     *
     * Segue a convencao que o Meta bundle ja usa para comentario (`comment:`): prefixo
     * curto no proprio destinatario, porque e o destinatario que a caixa consulta para
     * decidir se abre o compositor.
     */
    public const UNRESOLVED_PREFIX = 'jid:';

    private const PHONE_SERVER = 's.whatsapp.net';

    public string $raw;
    public string $user;
    public string $server;

    public function __construct(string $raw)
    {
        $this->raw = trim($raw);
        if ('' === $this->raw) {
            throw new \InvalidArgumentException('Uma mensagem sem remetente nao diz de quem e a conversa.');
        }

        $at = strrpos($this->raw, '@');
        $user = false === $at ? $this->raw : substr($this->raw, 0, $at);
        $this->server = false === $at ? '' : substr($this->raw, $at + 1);

        // O whatsmeow escreve o aparelho depois de dois pontos (`5511999999999:12@...`).
        // O aparelho e de onde a mensagem saiu, nao de quem: mante-lo abriria uma conversa
        // por celular e notebook da mesma pessoa.
        $colon = strpos($user, ':');
        $this->user = false === $colon ? $user : substr($user, 0, $colon);
    }

    /**
     * Se este JID sequer promete um telefone. Se promete, quem confere de verdade e o
     * PhoneNormalizer -- esta guarda so evita entregar a ele o que nunca foi numero.
     */
    public function carriesPhone(): bool
    {
        return self::PHONE_SERVER === $this->server && 1 === preg_match('/^[0-9]{8,15}$/', $this->user);
    }

    /**
     * O destinatario de uma conversa que nao tem telefone resolvido.
     *
     * Guarda o JID inteiro, e nao so os digitos: quem for diagnosticar precisa ver de
     * qual identificador a conversa nasceu, e o prefixo garante que nenhum envio confunda
     * isto com um numero.
     */
    public function unresolvedRecipient(): string
    {
        return self::UNRESOLVED_PREFIX.$this->raw;
    }

    public static function isUnresolved(string $recipient): bool
    {
        return str_starts_with($recipient, self::UNRESOLVED_PREFIX);
    }
}
