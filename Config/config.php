<?php

declare(strict_types=1);

use MauticPlugin\MauticWhatsQrBundle\Controller\WebhookController;

return [
    'name'        => 'Mautic WhatsApp QR',
    'description' => 'Numero de WhatsApp pareado por QR Code, atendido pela mesma caixa dos canais oficiais da Meta.',
    'version'     => '0.1.0',
    'author'      => 'Raphael Cangucu',
    'routes'      => [
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
];
