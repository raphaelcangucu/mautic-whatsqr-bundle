<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

/**
 * Os tres tipos de evento que o servico manda, e nada alem deles.
 *
 * A lista existe por escrito para que a porta possa recusar o que nao reconhece em vez
 * de gravar e seguir: um tipo desconhecido chegando aqui e um servico mais novo que o
 * plugin, e aceita-lo calado faria a caixa perder o evento sem ninguem saber -- ficaria
 * na tabela, marcado como recebido, esperando um processador que nao existe.
 *
 * Classe com constantes, e nao enum, pelo mesmo motivo do SessionState: o valor
 * atravessa JSON nas duas pontas, e um enum obrigaria a converter toda vez para guardar
 * de volta a mesma string.
 *
 * Os valores sao os de session.NoticeKind, em service/session/manager.go. Um nome
 * digitado diferente aqui nao quebra teste nenhum do lado de la -- so silencia a
 * entrada em producao.
 */
final class WebhookEventType
{
    public const MESSAGE = 'message';
    public const STATUS = 'status';
    public const SESSION = 'session';

    private const KNOWN = [self::MESSAGE, self::STATUS, self::SESSION];

    public static function isKnown(string $type): bool
    {
        return in_array($type, self::KNOWN, true);
    }

    /**
     * @return list<string>
     */
    public static function all(): array
    {
        return self::KNOWN;
    }
}
