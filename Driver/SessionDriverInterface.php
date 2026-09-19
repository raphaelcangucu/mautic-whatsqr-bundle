<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\SentMessage;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;

/**
 * O motor de um numero, visto de dentro do plugin.
 *
 * Cinco metodos, um por gesto que o plugin ja faz hoje: abrir para parear, mostrar o
 * QR enquanto ele dura, desligar, mandar texto, e perguntar o que o servico ve. Nao ha
 * metodo aqui esperando um motor futuro -- interface larga vira metodo vazio que alguem
 * chama por engano.
 *
 * A escolha e por numero, e nao global: em canal nao homologado o comportamento varia
 * por chip, e um que comece a cair precisa trocar de motor sem derrubar os outros.
 *
 * Sobre o que cada implementacao lanca, porque disto depende o que o atendente ve:
 * canal fora do ar que volta sozinho e ChannelTemporarilyUnavailable; recusa que nao
 * muda sozinha e \DomainException. A OutboundQueue le os dois tipos, nao a mensagem.
 */
interface SessionDriverInterface
{
    public function openSession(MetaAsset $asset): SessionState;

    /**
     * O QR de agora, ou null quando a sessao nao esta em pareamento. Null e resposta, e
     * nao erro: a tela de Parear distingue "ainda nao chegou" de "ja conectou".
     */
    public function pairingQr(MetaAsset $asset): ?string;

    public function closeSession(MetaAsset $asset): void;

    public function sendText(MetaAsset $asset, string $to, string $text, string $requestId): SentMessage;

    /**
     * O que o servico ve de todas as sessoes que ele mantem, por id de sessao.
     *
     * Nao recebe asset, e devolve todas de uma vez, porque e assim que o `/health`
     * responde: o desenho cortou de proposito a rota por sessao para nao ter a mesma
     * informacao por dois caminhos. Com ate cinco numeros, uma pergunta responde a tela
     * inteira -- e perguntar numero por numero seriam cinco idas de dez segundos
     * justamente quando o servico esta fora do ar, que e quando a tela e aberta.
     *
     * A tela nao usa isto para a situacao de cada numero: essa vem do estado gravado, que
     * chega pelo webhook e e o mesmo que a fila le. Isto existe para a unica situacao que
     * o estado gravado nao pode conhecer -- SessionState::AMBIGUOUS_CREDENTIAL, o numero
     * cuja sessao nunca abriu e que por isso nunca teve evento de sessao para mandar.
     *
     * @return array<string, SessionState>
     */
    public function serviceSessions(): array;
}
