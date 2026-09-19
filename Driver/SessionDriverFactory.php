<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use Mautic\CoreBundle\Helper\CoreParametersHelper;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Security\CredentialVault;
use Symfony\Contracts\HttpClient\HttpClientInterface;

/**
 * Quem escolhe o motor de um numero, e a unica porta por onde essa escolha e gravada.
 *
 * A escolha e por numero e nao do plugin inteiro: em canal nao homologado o
 * comportamento varia por chip, e um que comece a cair precisa trocar de motor ou de
 * processo sozinho, sem mexer nos outros quatro. A configuracao global guarda so o
 * padrao para numeros novos -- dai ela ser lida aqui como fallback, e nao como regra.
 *
 * As duas metades desta classe existem juntas de proposito. `configure()` e a porta de
 * escrita e `forAsset()` a de leitura; se a recusa morasse so na leitura, escolher um
 * motor inexistente seria aceito na tela e estouraria horas depois, no envio, com um
 * cliente do outro lado esperando resposta.
 */
final class SessionDriverFactory
{
    public const ENGINE_WHATSMEOW = 'whatsmeow';
    public const ENGINE_BAILEYS = 'baileys';

    /**
     * O motor padrao do plugin, valido so para numero que ainda nao gravou o seu.
     */
    public const PARAMETER_DEFAULT_ENGINE = 'whatsqr_default_engine';

    /**
     * Os campos por numero moram em `settings` do asset, que e um saco compartilhado com
     * o Meta bundle -- dai o prefixo. O id da sessao nao esta aqui: ele mora em
     * `external_id`, porque e o identificador do asset e nao uma preferencia dele.
     */
    public const SETTING_ENGINE = 'whatsqr_engine';
    public const SETTING_BASE_URI = 'whatsqr_base_uri';
    public const SETTING_TOKEN = 'whatsqr_token';
    public const SETTING_WEBHOOK_SECRET = 'whatsqr_webhook_secret';

    /**
     * Tudo que o desenho nomeia, inclusive o que ainda nao existe. Listar o baileys e
     * honesto -- ele esta no desenho e chegara um dia; o que nao pode e aceita-lo.
     */
    private const OFFERED = [self::ENGINE_WHATSMEOW, self::ENGINE_BAILEYS];

    /**
     * O que esta fabrica sabe construir hoje. Esta lista e uma so, e e a mesma que a tela
     * consulta e que a gravacao exige: duas listas em lugares diferentes envelhecem
     * separadas, e a que envelhece calada e sempre a que deixa passar.
     */
    private const IMPLEMENTED = [self::ENGINE_WHATSMEOW];

    public function __construct(
        private readonly HttpClientInterface $http,
        private readonly CredentialVault $vault,
        private readonly CoreParametersHelper $parameters,
    ) {
    }

    /**
     * O que a tela de configuracao oferece na lista de motores.
     *
     * @return list<string>
     */
    public function offeredEngines(): array
    {
        return self::OFFERED;
    }

    /**
     * @return list<string>
     */
    public function implementedEngines(): array
    {
        return self::IMPLEMENTED;
    }

    public function defaultEngine(): string
    {
        $engine = trim((string) $this->parameters->get(self::PARAMETER_DEFAULT_ENGINE, self::ENGINE_WHATSMEOW));

        return '' === $engine ? self::ENGINE_WHATSMEOW : $engine;
    }

    /**
     * A recusa da configuracao, e o motivo por escrito.
     *
     * Nao pede asset, endereco nem token de proposito: assim a tela pode perguntar com o
     * formulario ainda aberto, antes de existir numero para o motor escolhido. O que faz
     * disto recusa na configuracao, e nao no envio, e este metodo ser chamado por
     * `configure()` -- a unica porta que grava o motor em `settings`.
     */
    public function assertEngineIsImplemented(string $engine): void
    {
        $engine = trim($engine);
        if (in_array($engine, self::IMPLEMENTED, true)) {
            return;
        }

        // \DomainException, e nao InvalidArgument: escolher um motor que nao existe e uma
        // decisao recusada, nao um argumento malformado -- e a OutboundQueue ja le este
        // tipo como recusa definitiva caso um destes escape ate la.
        if (in_array($engine, self::OFFERED, true)) {
            throw new \DomainException(sprintf(
                'O motor "%s" esta no desenho mas ainda nao tem implementacao neste plugin; escolha "%s".',
                $engine,
                implode('", "', self::IMPLEMENTED)
            ));
        }

        throw new \DomainException(sprintf(
            'O motor "%s" nao existe neste plugin e por isso nao tem implementacao; escolha "%s".',
            $engine,
            implode('", "', self::IMPLEMENTED)
        ));
    }

    /**
     * Grava a configuracao daquele numero, ou recusa sem gravar nada.
     *
     * Recebe as credenciais em claro e as sela aqui, porque a alternativa -- cada tela
     * selar a sua -- e como uma delas acaba gravando o token puro em `settings`, que
     * aparece em dump, em backup e na leitura de `settings` do MCP. Segredo do webhook
     * nulo mantem o que ja estava: trocar o motor de um numero nao deveria obrigar a
     * regerar o segredo e reconfigurar o servico do outro lado.
     */
    public function configure(
        MetaAsset $asset,
        string $engine,
        string $baseUri,
        string $token,
        ?string $webhookSecret = null,
    ): void {
        $engine = '' === trim($engine) ? $this->defaultEngine() : trim($engine);
        // Antes de qualquer escrita: configuracao pela metade e o estado que faz a tela
        // dizer "salvo" e o envio dizer "nao saiu".
        $this->assertEngineIsImplemented($engine);

        $baseUri = trim($baseUri);
        if ('' === $baseUri) {
            throw new \DomainException('Um numero por QR precisa do endereco do servico que mantem a sessao dele.');
        }
        if ('' === trim($token)) {
            throw new \DomainException('Um numero por QR precisa do token do servico; sem ele toda chamada volta recusada.');
        }

        $settings = $asset->getSettings();
        $settings[self::SETTING_ENGINE] = $engine;
        $settings[self::SETTING_BASE_URI] = $baseUri;
        $settings[self::SETTING_TOKEN] = $this->vault->seal($token);
        if (null !== $webhookSecret) {
            $settings[self::SETTING_WEBHOOK_SECRET] = $this->vault->seal($webhookSecret);
        }

        $asset->setSettings($settings);
    }

    /**
     * O adaptador daquele numero, com o endereco e o token daquele numero.
     *
     * Instancia nova a cada chamada, e nao um cache por motor: dois numeros no mesmo
     * motor apontam para processos diferentes, e um adaptador compartilhado falaria pelo
     * endereco do ultimo que pediu.
     */
    public function forAsset(MetaAsset $asset): SessionDriverInterface
    {
        if (AssetType::WhatsAppQrSession !== $asset->getType()) {
            // Um numero do Graph passando por aqui sairia pelo servico nao homologado sem
            // que ninguem tivesse pedido isso -- e o risco central deste canal e banimento.
            throw new \DomainException(sprintf('O asset "%s" nao e um numero por QR Code.', $asset->getName()));
        }

        $settings = $asset->getSettings();
        $engine = trim((string) ($settings[self::SETTING_ENGINE] ?? ''));
        // Numero novo, ainda sem motor gravado, herda o padrao do plugin. Ele passa pela
        // mesma recusa: um padrao global mal configurado nao vira excecao de envio.
        $engine = '' === $engine ? $this->defaultEngine() : $engine;
        $this->assertEngineIsImplemented($engine);

        $baseUri = trim((string) ($settings[self::SETTING_BASE_URI] ?? ''));
        if ('' === $baseUri) {
            throw new \DomainException(sprintf('O numero "%s" nao tem endereco de servico configurado.', $asset->getName()));
        }

        $sealed = (string) ($settings[self::SETTING_TOKEN] ?? '');
        if ('' === $sealed) {
            throw new \DomainException(sprintf('O numero "%s" nao tem token de servico configurado.', $asset->getName()));
        }

        return match ($engine) {
            self::ENGINE_WHATSMEOW => new WhatsMeowDriver($this->http, $baseUri, $this->vault->open($sealed)),
        };
    }

    /**
     * O segredo com que o servico assina o webhook daquele numero, aberto.
     *
     * Mora aqui, e nao no controlador, porque e o mesmo par de campos por numero que esta
     * classe grava: quem conhece a chave de `settings` e quem a selou.
     */
    public function webhookSecret(MetaAsset $asset): string
    {
        $sealed = (string) ($asset->getSettings()[self::SETTING_WEBHOOK_SECRET] ?? '');
        if ('' === $sealed) {
            throw new \DomainException(sprintf('O numero "%s" nao tem segredo de webhook configurado.', $asset->getName()));
        }

        return $this->vault->open($sealed);
    }
}
