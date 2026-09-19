<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

/**
 * O miolo do cartao de parear, num instante.
 *
 * Um componente, tres estados, e o que muda e isto -- nao a tela. Tres telas separadas
 * obrigariam cada uma a repetir o cabecalho, o nome do numero e o caminho de volta, e a
 * terceira copia e a que envelhece diferente das outras duas.
 */
final readonly class PairingView
{
    /** Esperando alguem escanear: ha QR na tela. */
    public const WAITING = 'waiting';
    /** Escaneou: o numero ja recebe. */
    public const CONNECTED = 'connected';
    /** Nao deu. O que distingue os casos e $cause. */
    public const NOT_DONE = 'not_done';

    /** O codigo venceu sem ninguem escanear. Nada foi criado; outro codigo resolve. */
    public const CAUSE_EXPIRED = 'expired';
    /** O WhatsApp desfez o pareamento. Um scan novo resolve; o antigo nao volta. */
    public const CAUSE_UNPAIRED = 'unpaired';
    /** O WhatsApp recusou. Outro codigo repete a recusa. */
    public const CAUSE_REFUSED = 'refused';
    /** Ha duas credenciais no disco do servico para este numero. */
    public const CAUSE_AMBIGUOUS = 'ambiguous_credential';
    /**
     * O servico nao respondeu. Nao e o WhatsApp recusando nada -- e por isso que esta
     * causa existe separada: tentar de novo quando o processo voltar e exatamente o
     * certo, e junta-la a recusa esconderia o botao no unico caso em que ele resolve
     * sozinho.
     */
    public const CAUSE_SERVICE_DOWN = 'service_down';

    public function __construct(
        public string $stage,
        public ?string $qr = null,
        public ?string $jid = null,
        public ?string $cause = null,
        public ?string $reason = null,
        /**
         * Se a tela oferece o botao de gerar outro codigo.
         *
         * Nao e derivado de $cause pelo Twig de proposito: quem decide isto e uma funcao
         * com teste, porque e aqui que o erro mora. Um `{% if cause != 'refused' %}` no
         * template passa a valer para toda causa nova sem ninguem reparar.
         */
        public bool $offersRetry = false,
    ) {
    }
}
