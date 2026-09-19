<?php

declare(strict_types=1);

use Mautic\CoreBundle\DependencyInjection\MauticCoreExtension;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Infrastructure\TransportResolver;
use MauticPlugin\MauticWhatsQrBundle\Transport\QrTransport;
use Symfony\Component\DependencyInjection\Loader\Configurator\ContainerConfigurator;

return function (ContainerConfigurator $configurator): void {
    $services = $configurator->services()
        ->defaults()
        ->autowire()
        ->autoconfigure()
        ->public();

    $excludes = MauticCoreExtension::DEFAULT_EXCLUDES;
    // Domain guarda objeto de valor, com construtor de escalares. O autowire nao tem como
    // adivinhar uma string, entao registra-los quebra o COMPILE do container — e container que
    // nao compila derruba o painel inteiro do Mautic, nao so este plugin. A lista do core nao
    // cobre Domain porque nem todo plugin tem um. Tests pela mesma razao: nao e codigo de runtime.
    $excludes[] = 'Domain';
    // O adaptador tambem nao e servico: quem o constroi e a fabrica, um por numero, com o
    // endereco e o token daquele numero. Registra-lo aqui pediria ao autowire para adivinhar
    // duas strings — e, pior, criaria um adaptador unico compartilhado, que falaria pelo
    // endereco do ultimo numero que pediu.
    $excludes[] = 'Driver/WhatsMeowDriver.php';

    // O controlador do webhook precisa existir como servico: a rota o referencia
    // por Classe::metodo, e sem registro o roteador so descobre isso na requisicao.
    $services->load('MauticPlugin\\MauticWhatsQrBundle\\', '../')
        ->exclude('../{'.implode(',', $excludes).'}');

    // A etiqueta e a unica coisa que liga a saida deste plugin ao Meta bundle: o
    // TransportResolver de la recebe os transportes por esta tag, indexados pelo valor do
    // AssetType, e escolhe o do asset. Sem a linha abaixo o QrTransport existiria como
    // servico e nunca seria chamado — o primeiro envio por um numero de QR morreria no
    // "No WhatsApp transport is registered for asset type", que e erro de configuracao
    // aparecendo como falha de envio, com um cliente do outro lado esperando resposta.
    // O tipo vem da enum, e nao da string: o dia em que o valor mudar la, isto acompanha.
    $services->set(QrTransport::class)
        ->tag(TransportResolver::TAG, ['asset_type' => AssetType::WhatsAppQrSession->value]);
};
