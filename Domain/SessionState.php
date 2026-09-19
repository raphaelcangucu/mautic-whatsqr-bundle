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
     * A sexta palavra: o numero que tem duas credenciais no disco do servico e cuja
     * sessao o `Open` de la se RECUSA a abrir -- um DELETE que falhou no meio, um backup
     * restaurado por cima. Escolher uma das duas abriria a errada em metade das vezes,
     * calada, entao ele nao escolhe.
     *
     * Fica fora de KNOWN de proposito, e a razao e o caminho por onde ela chega. KNOWN e
     * a lista do que vem pelo WEBHOOK -- e por isso que o gravador e a varredura
     * consultam essa lista. Este estado nao vem por webhook e nao pode vir: nao ha sessao
     * aberta, logo nao ha evento de sessao para o servico mandar. Ele so existe quando
     * alguem PERGUNTA ao servico, que e o que a tela de Conexoes faz no `/health`.
     * Junta-lo a KNOWN faria o gravador aceitar por webhook uma palavra que o servico
     * nunca manda por ali, e a lista deixaria de dizer o que diz.
     */
    public const AMBIGUOUS_CREDENTIAL = 'ambiguous_credential';

    /**
     * Tudo que pode descrever um numero na tela: as cinco do webhook mais a do `/health`.
     * E esta a lista que o construtor cobra, porque o objeto e o que a tela recebe.
     */
    private const REPORTABLE = [
        self::PAIRING,
        self::CONNECTED,
        self::RECONNECTING,
        self::LOGGED_OUT,
        self::FAILED,
        self::AMBIGUOUS_CREDENTIAL,
    ];

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

    /**
     * Se esta palavra e uma das que chegam pelo webhook. Quem grava e quem varre a fila
     * perguntam isto -- e nao `isReportable()` --, porque o que eles recebem e evento.
     */
    public static function isKnown(string $status): bool
    {
        return in_array($status, self::KNOWN, true);
    }

    public static function isReportable(string $status): bool
    {
        return in_array($status, self::REPORTABLE, true);
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
        if (!self::isReportable($status)) {
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

    /**
     * Se este numero espera alguem, e nao tempo.
     *
     * Estatica e sobre a palavra, e nao sobre o objeto, porque quem mais precisa da
     * resposta e a tela de Conexoes -- e la a situacao de um numero pode ser
     * ConnectionRow::UNKNOWN, que nao cabe num SessionState. Uma segunda lista dentro da
     * tela envelheceria separada desta, e a que envelhece calada e sempre a que deixa
     * passar.
     *
     * A credencial duplicada nao passa sozinha: nenhuma retentativa apaga do disco a
     * segunda credencial. `logged_out` tambem nao -- o WhatsApp desfez o pareamento e so
     * um scan novo resolve. `failed` e terminal por definicao. `reconnecting` e o oposto
     * disso e fica de fora: esperar e exatamente o certo a fazer ali, e uma tela que pede
     * socorro a cada oscilacao de linha deixa de ser lida.
     */
    public static function needsSomebody(string $status): bool
    {
        return in_array($status, [self::AMBIGUOUS_CREDENTIAL, self::LOGGED_OUT, self::FAILED], true);
    }
}
