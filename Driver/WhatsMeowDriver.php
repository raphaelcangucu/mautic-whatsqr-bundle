<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Application\Exception\ChannelTemporarilyUnavailable;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\SentMessage;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use MauticPlugin\MauticWhatsQrBundle\Infrastructure\SessionStreamDecoder;
use Symfony\Contracts\HttpClient\Exception\TransportExceptionInterface;
use Symfony\Contracts\HttpClient\HttpClientInterface;

/**
 * O adaptador do unico motor implementado: o servico em Go, que fala com o WhatsApp
 * pela biblioteca whatsmeow.
 *
 * O endereco e o token chegam prontos, por numero, porque e assim que a escolha de
 * motor do desenho funciona: cada chip pode apontar para um processo diferente. Quem
 * os le do asset e a fabrica; este objeto so sabe conversar.
 *
 * Toda a traducao mora aqui -- o dialeto do servico entra, SessionState e SentMessage
 * saem, e nenhum "status" cru atravessa para a tela ou para a fila.
 */
final class WhatsMeowDriver implements SessionDriverInterface, SessionProvisioningDriverInterface, ProfileImageDriverInterface, AttachmentDriverInterface, HistoryDriverInterface
{
    use AttachmentResponseTrait;
    /**
     * Teto de espera de uma chamada. Existe por causa do envio: sem teto, o servico
     * pendurado segura a requisicao HTTP do atendente ate o PHP desistir, e a tela fica
     * girando. Com teto, o estouro vira falha temporaria e a resposta entra na fila.
     */
    private const TIMEOUT_SECONDS = 10;

    private string $baseUri;

    public function requestHistory(MetaAsset $asset, array $anchors = []): void
    {
        $body = $this->request('POST', '/sessions/'.rawurlencode($this->sessionId($asset)).'/history', ['chats' => $anchors]);
        if ('requested' !== ($body['status'] ?? null)) {
            throw new \RuntimeException('The WhatsApp service did not acknowledge the history request.');
        }
    }

    public function __construct(
        private readonly HttpClientInterface $http,
        string $baseUri,
        private readonly string $token,
    ) {
        $this->baseUri = rtrim($baseUri, '/');
    }

    public function profileImage(MetaAsset $asset, string $recipient): ?\MauticPlugin\MauticWhatsQrBundle\Domain\ProfileImage
    {
        $url = $this->baseUri.'/sessions/'.rawurlencode($this->sessionId($asset)).'/avatar?'.http_build_query(['to' => $recipient]);
        $response = $this->http->request('GET', $url, [
            'auth_bearer' => $this->token, 'timeout' => 9, 'max_duration' => 10,
            'max_redirects' => 0, 'buffer' => false,
        ]);
        try {
            if (200 !== $response->getStatusCode()) { return null; }
            $contents = '';
            foreach ($this->http->stream($response, 9) as $chunk) {
                if ($chunk->isTimeout()) { return null; }
                $contents .= $chunk->getContent();
                if (strlen($contents) > 262144) { return null; }
            }
            $info = @getimagesizefromstring($contents);
            $mime = is_array($info) ? ($info['mime'] ?? '') : '';
            if (!in_array($mime, ['image/jpeg', 'image/png', 'image/webp'], true) || $info[0] > 2048 || $info[1] > 2048) { return null; }

            return new \MauticPlugin\MauticWhatsQrBundle\Domain\ProfileImage($contents, $mime);
        } finally {
            $response->cancel();
        }
    }

    public function openSession(MetaAsset $asset): SessionState
    {
        $body = $this->request('POST', '/sessions', ['id' => $this->sessionId($asset)]);

        return $this->toSessionState($this->sessionId($asset), $body);
    }

    /** Provision the webhook secret before opening a new mobile session. */
    public function registerSession(MetaAsset $asset, string $secret): void
    {
        $this->request('POST', '/sessions/'.rawurlencode($this->sessionId($asset)).'/configuration', ['webhook_secret' => $secret]);
    }

    public function pairingQr(MetaAsset $asset): ?string
    {
        $id = $this->sessionId($asset);
        // 409 e "essa sessao nao esta em pareamento", que e resposta e nao erro: ja
        // conectou, ou ainda nao abriu. Devolver null aqui deixa a tela decidir o que
        // dizer, em vez de transformar um estado normal em excecao.
        $body = $this->request('GET', sprintf('/sessions/%s/qr', rawurlencode($id)), null, [409]);
        $qr = trim((string) ($body['qr'] ?? ''));

        return '' === $qr ? null : $qr;
    }

    public function closeSession(MetaAsset $asset): void
    {
        // 404 e aceito porque desligar o que ja nao existe alcancou o que se queria.
        // Nao vale para 500: a rota devolve isso quando desconectou mas a credencial
        // continua no disco, e engolir isso diria que o numero foi desligado quando ele
        // ainda pode falar.
        $this->request('DELETE', sprintf('/sessions/%s', rawurlencode($this->sessionId($asset))), null, [404]);
    }

    public function sendText(MetaAsset $asset, string $to, string $text, string $requestId): SentMessage
    {
        $body = $this->request('POST', sprintf('/sessions/%s/messages', rawurlencode($this->sessionId($asset))), [
            'to'         => $to,
            'text'       => $text,
            'request_id' => $requestId,
        ]);

        return new SentMessage(trim((string) ($body['message_id'] ?? '')), $requestId);
    }

    /**
     * @return array<string, SessionState>
     */
    public function serviceSessions(): array
    {
        $body = $this->request('GET', '/health');
        $sessions = $body['sessions'] ?? [];
        if (!is_array($sessions)) {
            return [];
        }

        $states = [];
        foreach ($sessions as $entry) {
            if (!is_array($entry)) {
                continue;
            }
            $id = trim((string) ($entry['id'] ?? ''));
            if ('' === $id) {
                continue;
            }
            try {
                $states[$id] = $this->toSessionState($id, $entry);
            } catch (\RuntimeException) {
                // Uma palavra que este plugin nao conhece e um servico mais novo que ele,
                // e aqui ela nao pode derrubar a resposta inteira: a situacao de cada
                // numero na tela vem do estado gravado, nao daqui. Estourar faria o
                // sexto numero, que fala a palavra nova, apagar a linha dos outros cinco
                // -- e a tela existe justamente para ser lida quando algo esta errado.
                continue;
            }
        }

        return $states;
    }

    public function watchSession(MetaAsset $asset, callable $onState, callable $onHeartbeat): void
    {
        $id = $this->sessionId($asset);
        $response = $this->http->request('GET', $this->baseUri.'/sessions/'.rawurlencode($id).'/events', [
            'auth_bearer' => $this->token,
            'headers' => ['Accept' => 'text/event-stream'],
            'buffer' => false, 'timeout' => 10, 'max_duration' => 28, 'max_redirects' => 0,
        ]);
        try {
            if (200 !== $response->getStatusCode()) {
                throw new \RuntimeException('Atualizações da conexão temporariamente indisponíveis.');
            }
            $decoder = new SessionStreamDecoder();
            foreach ($this->http->stream($response, 1.0) as $chunk) {
                if ($chunk->isTimeout()) {
                    if (false === $onHeartbeat()) { return; }
                    continue;
                }
                foreach ($decoder->push($chunk->getContent()) as $frame) {
                    if ('session' === $frame['event']) {
                        $body = $this->decode($frame['data']);
                        // A stream must never deliver another account's state/QR.
                        if (($body['id'] ?? null) !== $id) {
                            throw new \RuntimeException('Evento de conexão pertence a outra sessão.');
                        }
                        $state = 'ready' === ($body['status'] ?? '') ? null : $this->toSessionState($id, $body);
                        if (false === $onState($state)) { return; }
                    } elseif ('rotate' === $frame['event']) {
                        return;
                    } elseif (false === $onHeartbeat()) {
                        return;
                    }
                }
            }
        } finally {
            $response->cancel();
        }
    }

    /**
     * O id da sessao e o externalId do asset. Uma sessao por QR nao tem numero no Graph
     * -- AssetType::isGraphAsset() ja diz isso --, entao o campo guarda o id nao
     * enumeravel pelo qual o servico, a configuracao e o webhook reconhecem este chip.
     */
    private function sessionId(MetaAsset $asset): string
    {
        $id = trim($asset->getExternalId());
        if ('' === $id) {
            // Sem id nao ha a quem pedir nada, e isso nao melhora com o tempo.
            throw new \DomainException('Este numero nao tem id de sessao gravado; refaca o pareamento.');
        }

        return $id;
    }

    private function toSessionState(string $sessionId, array $body): SessionState
    {
        $status = trim((string) ($body['status'] ?? ''));
        $known = [
            'pairing'      => SessionState::PAIRING,
            'connected'    => SessionState::CONNECTED,
            'reconnecting' => SessionState::RECONNECTING,
            'logged_out'   => SessionState::LOGGED_OUT,
            'failed'       => SessionState::FAILED,
            // So o `/health` manda esta: ela descreve um numero cuja sessao nao existe,
            // e por isso nunca sai de POST /sessions nem de evento de sessao.
            'ambiguous_credential' => SessionState::AMBIGUOUS_CREDENTIAL,
        ];
        if (!isset($known[$status])) {
            // Um motor que passou a falar outra palavra e um problema de contrato, nao do
            // envio. RuntimeException de proposito: e o que a fila le como desfecho
            // desconhecido, e desconhecido e exatamente o que isto e.
            throw new \RuntimeException(sprintf('O servico devolveu um estado que este plugin nao conhece: "%s".', $status));
        }

        $qr = trim((string) ($body['qr'] ?? ''));
        $jid = trim((string) ($body['jid'] ?? ''));
        $reason = trim((string) ($body['reason'] ?? ''));

        return new SessionState(
            '' !== trim((string) ($body['id'] ?? '')) ? trim((string) $body['id']) : $sessionId,
            $known[$status],
            '' === $qr ? null : $qr,
            '' === $jid ? null : $jid,
            '' === $reason ? null : $reason,
        );
    }

    /**
     * @param array<string,mixed>|null $payload
     * @param list<int>                $tolerate codigos 4xx que o chamador trata como resposta
     *
     * @return array<string,mixed>
     */
    private function request(string $method, string $path, ?array $payload = null, array $tolerate = []): array
    {
        $options = ['auth_bearer' => $this->token, 'timeout' => self::TIMEOUT_SECONDS];
        if (null !== $payload) {
            $options['json'] = $payload;
        }

        try {
            $response = $this->http->request($method, $this->baseUri.$path, $options);
            $status = $response->getStatusCode();
            $raw = $response->getContent(false);
        } catch (TransportExceptionInterface $transport) {
            // Servico fora do ar, conexao recusada, tempo esgotado. Esta e a linha que o
            // desenho inteiro existe para ter: qualquer outra excecao aqui faz a
            // OutboundQueue classificar como definitiva, e o atendente le "nao saiu" em
            // dois segundos de uma mensagem que sairia sozinha quando o numero voltasse.
            throw new ChannelTemporarilyUnavailable(
                sprintf('O servico de WhatsApp por QR nao respondeu (%s %s).', $method, $path),
                0,
                $transport
            );
        }

        if ($status >= 200 && $status < 300) {
            return $this->decode($raw);
        }
        if (in_array($status, $tolerate, true)) {
            return [];
        }

        $message = trim((string) ($this->decode($raw, false)['error'] ?? ''));
        if ('' === $message) {
            $message = sprintf('HTTP %d', $status);
        }

        // A fronteira e o proprio codigo, e nao o campo "temporary" do corpo: o servico
        // ja alinha os dois, e ler duas fontes que podem discordar cria um caso em que
        // ninguem sabe qual venceu. 408 e 429 entram porque um proxy no caminho pode
        // devolve-los, e os dois passam sozinhos.
        if ($status >= 500 || 408 === $status || 429 === $status) {
            throw new ChannelTemporarilyUnavailable(sprintf('O servico de WhatsApp por QR esta indisponivel: %s', $message));
        }

        // 4xx e recusa do proprio servico que nao muda sozinha: sessao desconhecida,
        // corpo invalido, numero sem segredo, token errado. \DomainException porque a
        // OutboundQueue a le como falha definitiva -- retentar isto ocuparia a fila para
        // sempre com um envio que nunca vai dar certo, e a mensagem certa para o
        // atendente e "nao saiu".
        throw new \DomainException(sprintf('O servico de WhatsApp por QR recusou: %s', $message));
    }

    /**
     * @return array<string,mixed>
     */
    private function decode(string $raw, bool $strict = true): array
    {
        if ('' === trim($raw)) {
            return [];
        }
        try {
            $decoded = json_decode($raw, true, 512, JSON_THROW_ON_ERROR);
        } catch (\JsonException $malformed) {
            if (!$strict) {
                return [];
            }
            // Resposta ilegivel de um servico que respondeu: pode ter enviado. Nao vira
            // falha temporaria de proposito -- retentar as cegas duplicaria a mensagem.
            throw new \RuntimeException('O servico de WhatsApp por QR devolveu uma resposta ilegivel.', 0, $malformed);
        }

        return is_array($decoded) ? $decoded : [];
    }
}
