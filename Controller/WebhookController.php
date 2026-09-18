<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Controller;

use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

final class WebhookController
{
    /**
     * Esqueleto. A selecao de chave, a conferencia da assinatura sobre
     * timestamp+corpo, a recusa por replay e o dedupe sao a tarefa 4 do plano.
     * Ate la esta rota nao le o corpo e nao grava nada: responder 200 sem
     * processar e preferivel a processar sem conferir quem assinou.
     */
    public function handle(Request $request): Response
    {
        return new JsonResponse(['received' => true]);
    }
}
