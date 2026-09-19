<?php

declare(strict_types=1);

use MauticPlugin\MauticWhatsQrBundle\Controller\WebhookController;

return [
    'name'        => 'Mautic WhatsApp QR',
    'description' => 'Numero de WhatsApp pareado por QR Code, atendido pela mesma caixa dos canais oficiais da Meta.',
    'version'     => '0.1.0',
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
