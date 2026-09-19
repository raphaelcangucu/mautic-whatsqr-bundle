<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use Psr\Log\LoggerInterface;

/**
 * A unica pergunta que a tela de Conexoes faz ao servico, e o cuidado para que ela seja
 * uma so.
 *
 * A tela nao depende disto para funcionar: a situacao de cada numero vem do estado
 * gravado, que chega pelo webhook. Isto existe para o caso que o estado gravado nao pode
 * conhecer -- o numero com duas credenciais no disco, cuja sessao nunca abriu e que por
 * isso nunca teve evento de sessao para mandar.
 *
 * Duas decisoes moram aqui, e as duas sao sobre a mesma coisa: esta tela e aberta do
 * celular quando um numero cai, que e justamente quando o servico pode estar fora do ar.
 *
 * A primeira: uma pergunta por endereco de servico, e nao por numero. Cada chamada tem
 * teto de dez segundos; cinco numeros no mesmo processo seriam cinquenta segundos de tela
 * girando para responder cinco vezes a mesma coisa.
 *
 * A segunda: falha aqui nao derruba a tela. Se o servico nao responder, a tela se vira
 * com o estado gravado -- que e a fonte principal de qualquer jeito -- e o atendente
 * continua vendo quantas respostas estao paradas em cada numero. Uma tela que so abre
 * quando o servico esta de pe e uma tela que nunca abre no dia em que ela importa.
 */
final class ServiceHealth
{
    public function __construct(
        private readonly SessionDriverFactory $drivers,
        private readonly LoggerInterface $logger,
    ) {
    }

    /**
     * O que o servico de cada numero ve, por id de sessao.
     *
     * @param iterable<MetaAsset> $assets
     *
     * @return array<string, SessionState>
     */
    public function forAssets(iterable $assets): array
    {
        $asked = [];
        $states = [];

        foreach ($assets as $asset) {
            if (!$asset instanceof MetaAsset) {
                continue;
            }
            // O endereco e a identidade do processo, e ele e legivel sem abrir credencial
            // nenhuma -- ao contrario do token, que esta selado. Agrupar por ele e o que
            // faz cinco numeros no mesmo servico custarem uma pergunta.
            $address = trim((string) ($asset->getSettings()[SessionDriverFactory::SETTING_BASE_URI] ?? ''));
            if ('' === $address || isset($asked[$address])) {
                continue;
            }
            $asked[$address] = true;

            try {
                foreach ($this->drivers->forAsset($asset)->serviceSessions() as $sessionId => $state) {
                    // Primeiro a responder ganha. Dois servicos anunciando o mesmo id de
                    // sessao e configuracao errada, nao um caso a resolver aqui -- e
                    // sobrescrever calado faria a tela mostrar o estado do processo que
                    // por acaso veio depois na lista.
                    $states[$sessionId] ??= $state;
                }
            } catch (\Throwable $unreachable) {
                // Nivel de aviso, e nao de erro: o servico fora do ar ja aparece na tela
                // pelo estado gravado de cada numero, e esta pergunta e a segunda.
                $this->logger->warning(sprintf(
                    'whatsqr: o servico em "%s" nao respondeu ao /health (%s) -- a tela de Conexoes segue pelo estado gravado',
                    $address,
                    $unreachable->getMessage(),
                ));
            }
        }

        return $states;
    }
}
