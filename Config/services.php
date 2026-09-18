<?php

declare(strict_types=1);

use Mautic\CoreBundle\DependencyInjection\MauticCoreExtension;
use Symfony\Component\DependencyInjection\Loader\Configurator\ContainerConfigurator;

return function (ContainerConfigurator $configurator): void {
    $services = $configurator->services()
        ->defaults()
        ->autowire()
        ->autoconfigure()
        ->public();

    $excludes = MauticCoreExtension::DEFAULT_EXCLUDES;

    // O controlador do webhook precisa existir como servico: a rota o referencia
    // por Classe::metodo, e sem registro o roteador so descobre isso na requisicao.
    $services->load('MauticPlugin\\MauticWhatsQrBundle\\', '../')
        ->exclude('../{'.implode(',', $excludes).'}');
};
