<?php

declare(strict_types=1);

use MauticPlugin\MauticWhatsQrBundle\Controller\ConnectionsController;
use MauticPlugin\MauticWhatsQrBundle\Controller\WebhookController;

return [
    'name'        => 'Mautic WhatsApp QR',
    'description' => 'Numero de WhatsApp pareado por QR Code, atendido pela mesma caixa dos canais oficiais da Meta.',
    'version'     => '0.3.0',
    'author'      => 'Raphael Cangucu',
    'parameters'  => [
        // Padrao para numero novo, e so isso: o motor de cada numero mora no asset dele.
        // Um chip que comeca a cair troca de motor sozinho, sem arrastar os outros
        // quatro junto. O valor literal, e nao a constante da fabrica, porque este
        // arquivo e lido por include cru na carga de parametros, antes de haver
        // container -- ver SessionDriverFactory::PARAMETER_DEFAULT_ENGINE.
        'whatsqr_default_engine' => 'whatsmeow',
    ],
    'routes'      => [
        'main' => [
            'mautic_whatsqr_media' => [
                'path' => '/whatsqr/media/{messageId}',
                'controller' => \MauticPlugin\MauticWhatsQrBundle\Controller\MediaController::class.'::show',
                'method' => 'GET', 'requirements' => ['messageId' => '\\d+'],
            ],
            'mautic_whatsqr_avatar' => [
                'path' => '/whatsqr/avatars/{conversationId}',
                'controller' => \MauticPlugin\MauticWhatsQrBundle\Controller\AvatarController::class.'::show',
                'method' => 'GET', 'requirements' => ['conversationId' => '\\d+'],
            ],
            'mautic_whatsqr_connections' => [
                'path'       => '/whatsqr/connections',
                'controller' => ConnectionsController::class.'::index',
            ],
            'mautic_whatsqr_pair' => [
                'path'         => '/whatsqr/connections/{assetId}/pair',
                'controller'   => ConnectionsController::class.'::pair',
                'requirements' => ['assetId' => '\\d+'],
            ],
            'mautic_whatsqr_pair_start' => [
                'path' => '/whatsqr/connections/{assetId}/pair/start',
                'controller' => ConnectionsController::class.'::start',
                'method' => 'POST', 'requirements' => ['assetId' => '\d+'],
            ],
            'mautic_whatsqr_pair_status' => [
                'path' => '/whatsqr/connections/{assetId}/pair/status',
                'controller' => ConnectionsController::class.'::status',
                'method' => 'GET', 'requirements' => ['assetId' => '\d+'],
            ],
            'mautic_whatsqr_pair_events' => [
                'path' => '/whatsqr/connections/{assetId}/pair/events',
                'controller' => ConnectionsController::class.'::events',
                'method' => 'GET', 'requirements' => ['assetId' => '\\d+'],
            ],
            'mautic_whatsqr_pair_restart' => [
                'path'         => '/whatsqr/connections/{assetId}/pair/restart',
                'controller'   => ConnectionsController::class.'::restart',
                'method'       => 'POST',
                'requirements' => ['assetId' => '\\d+'],
            ],
        ],
        // A rota e publica porque quem chama e o servico em Go, que nao tem sessao
        // no Mautic. Quem autentica e a assinatura conferida no controlador, nao o
        // firewall — ver "Seguranca do webhook" no desenho.
        'public' => [
            'mautic_whatsqr_webhook' => [
                'path'       => '/whatsqr/webhook',
                'controller' => WebhookController::class.'::handle',
                'method'     => 'POST',
            ],
        ],
    ],
    'menu'        => [
        'main' => [
            'mautic.whatsqr.menu' => [
                // Prioridade logo abaixo da do Meta bundle: os numeros por QR sao lidos
                // junto dos oficiais, e um item longe do outro faz o atendente procurar.
                'route'     => 'mautic_whatsqr_connections',
                'access'    => 'meta:connections:view',
                'iconClass' => 'ri-qr-code-line',
                'priority'  => 19,
            ],
        ],
    ],
];
