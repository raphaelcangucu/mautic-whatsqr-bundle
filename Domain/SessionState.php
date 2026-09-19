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

    /**
     * A chave de `settings` do asset onde mora o estado gravado do numero, com uma das
     * cinco palavras acima.
     *
     * Mora aqui, e nao junto das outras chaves na SessionDriverFactory, porque nem quem
     * escreve nem quem le este campo e a fabrica: quem grava e o evento de sessao do
     * webhook, e quem le sao a varredura que expira resposta parada e o compositor da
     * caixa. A fabrica guarda o que ela mesma sela -- motor, endereco, token, segredo.
     *
     * Em `settings` e nao em `status` do asset por decisao do desenho: sair de `active`
     * fecha o compositor e faz o retry devolver 409, e um numero que caiu e vai voltar
     * nao deve calar a tela do atendente.
     */
    public const SETTING_STATUS = 'whatsqr_session_status';

    /**
     * A chave de `settings` onde mora o JID com que o numero pareou.
     *
     * Ao lado do estado porque e a outra metade da mesma pergunta: "este numero esta no
     * ar?" e "com qual chip?" so servem juntas. O servico ja recusa reconexao com JID
     * diferente (checkJID, em service/session/state.go); guardar o JID aqui e o que
     * permite ao Mautic contar a mesma historia sem ter de perguntar a ele -- e e o que a
     * tela de Conexoes mostra quando pede para escanear de novo, porque "escaneie de
     * novo" sem o chip por escrito nao diz com qual celular.
     */
    public const SETTING_JID = 'whatsqr_session_jid';

    /**
     * @return list<string>
     */
    public static function all(): array
    {
        return self::KNOWN;
    }

    public static function isKnown(string $status): bool
    {
        return in_array($status, self::KNOWN, true);
    }

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
        if (!self::isKnown($status)) {
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
