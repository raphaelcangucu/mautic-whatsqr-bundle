<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

/**
 * Uma linha da tela de Conexoes: um numero, o que ha com ele, quantas respostas estao
 * esperando e quando ele falou pela ultima vez.
 *
 * Objeto de valor e nao array por um motivo que a tela paga: `$row['fila']` escrito
 * errado no Twig nao estoura, imprime vazio -- e a coluna que imprime vazio calada e
 * justamente a que existe para gritar que tres clientes estao esperando.
 */
final readonly class ConnectionRow
{
    /**
     * O numero que nunca gravou estado: nunca pareou, ou o plugin subiu antes do servico.
     *
     * E uma situacao de verdade e nao um buraco a ser tapado com "conectado". Chutar para
     * o lado bom e o erro que a fila ja recusa cometer -- `ExpireQueuedCommand` pula quem
     * nao tem estado justamente porque ausencia nao e prova --, e a tela dizendo
     * "conectado" sobre um numero do qual nao se sabe nada seria a tela discordando da
     * fila sobre o mesmo numero.
     */
    public const UNKNOWN = 'unknown';

    public function __construct(
        public int $assetId,
        public string $name,
        public ?string $number,
        /** Uma palavra de SessionState, ou self::UNKNOWN. */
        public string $situation,
        /** Se este numero espera alguem, e nao tempo -- ver SessionState::needsSomebody(). */
        public bool $needsSomebody,
        /** O motivo por escrito, quando o servico deu um. */
        public ?string $reason,
        public int $queued,
        /**
         * Se a contagem bateu no teto da varredura e portanto e um piso.
         *
         * Existe porque um numero pequeno e errado nesta coluna e pior que nenhum: quem
         * le "12 esperando" fecha a tela achando que sabe o tamanho do problema.
         */
        public bool $queuedCapped,
        public ?\DateTimeInterface $lastMessageAt,
        public ?string $jid,
    ) {
    }
}
