#!/usr/bin/env bash
# Abre uma sessao nova e desenha o QR, em um comando so.
#
# Existe por causa de um numero: a janela de pareamento do whatsmeow dura menos de tres
# minutos -- seis codigos, o primeiro de 60 segundos e os outros de 20. Qualquer coisa que
# gaste esse tempo (procurar o comando certo, montar o curl, converter a imagem) gasta a
# janela inteira. Mandar um QR salvo "para escanear depois" nunca funciona.
#
# Por isso: um comando, rodado com a pessoa ja olhando a tela.
#
#   export WHATSQR_HOST=usuario@servidor
#   ./bin/parear-agora.sh [id-da-sessao]
#
# O token e lido no servidor, do arquivo de configuracao do servidor. Nunca viaja por
# aqui, nunca entra em linha de comando e nunca aparece em log.
set -euo pipefail

HOST="${WHATSQR_HOST:?defina WHATSQR_HOST, ex.: usuario@servidor}"
SESSAO="${1:-suporte}"
PORTA="${WHATSQR_PORT:-8088}"
CONF="${WHATSQR_CONF:-/home/forge/whatsqr/whatsqr.json}"
SAIDA="${WHATSQR_QR_OUT:-$PWD/qr-$SESSAO.png}"

if [[ ! "$SESSAO" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "RECUSADO: id de sessao com caracteres fora de [A-Za-z0-9_-]: $SESSAO" >&2
  exit 1
fi

# Fecha e reabre. Uma sessao que ja passou da janela continua existindo e continua
# dizendo "pareando"; reabrir e o unico jeito de ganhar codigo vivo.
CODIGO=$(ssh "$HOST" "
  T=\$(python3 -c \"import json;print(json.load(open('$CONF'))['token'])\")
  curl -s -m 10 -X DELETE -o /dev/null -H \"Authorization: Bearer \$T\" http://127.0.0.1:$PORTA/sessions/$SESSAO || true
  sleep 1
  curl -s -m 20 -X POST -H \"Authorization: Bearer \$T\" -H 'Content-Type: application/json' \
    -d '{\"id\":\"$SESSAO\"}' http://127.0.0.1:$PORTA/sessions \
    | python3 -c 'import sys,json;print(json.load(sys.stdin).get(\"qr\",\"\"))'
")

if [ -z "$CODIGO" ]; then
  echo "O servico abriu a sessao sem devolver codigo. Veja o /health antes de tentar de novo." >&2
  exit 1
fi

printf '%s' "$CODIGO" > "$SAIDA.txt"
RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
php -r '
require $argv[3];
$texto = file_get_contents($argv[1]);
$m = MauticPlugin\MauticWhatsQrBundle\Infrastructure\QrEncoder::matrix($texto);
$n = count($m); $e = 10; $marg = 4; $lado = ($n + 2*$marg) * $e;
$img = imagecreatetruecolor($lado, $lado);
imagefilledrectangle($img, 0, 0, $lado, $lado, imagecolorallocate($img, 255, 255, 255));
$preto = imagecolorallocate($img, 0, 0, 0);
for ($y = 0; $y < $n; $y++) for ($x = 0; $x < $n; $x++) if ($m[$y][$x])
    imagefilledrectangle($img, ($x+$marg)*$e, ($y+$marg)*$e, ($x+$marg+1)*$e-1, ($y+$marg+1)*$e-1, $preto);
imagepng($img, $argv[2]);
' "$SAIDA.txt" "$SAIDA" "$RAIZ/Infrastructure/QrEncoder.php"

rm -f "$SAIDA.txt"
echo "$SAIDA"
echo "Escaneie AGORA: WhatsApp > Aparelhos conectados > Conectar um aparelho."
echo "A janela fecha em menos de tres minutos."
