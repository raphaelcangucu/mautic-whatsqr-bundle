#!/usr/bin/env bash
# Mede quanto tempo uma resposta represada leva para sair depois que o numero volta.
#
# Existe por causa do E2E 4, que e o unico dos seis cujo valor esta num numero e nao num
# "funcionou". A ordem por conversa foi resolvida dentro do findDue(), e isso tem um custo
# conhecido: duas mensagens vencidas da mesma conversa nao saem no mesmo lote -- a segunda
# espera a varredura seguinte. Com varredura de minuto em minuto, tres respostas represadas
# gotejam por tres minutos. Com tres pode ser irrelevante; com quinze nao e, e a correcao
# seria no laco de despacho da OutboundQueue. Medir antes de decidir.
#
# Ninguem escreve isto no meio do teste, com o celular na mao e o cronometro correndo. Por
# isso esta aqui antes.
#
#   export WHATSQR_SQL_HOST=usuario@servidor
#   export WHATSQR_SQL_RELEASE=/caminho/de/uma/release/que/nao/atende/trafego
#   ./bin/medir-fila.sh <id-da-conversa> [segundos]
set -euo pipefail

HOST="${WHATSQR_SQL_HOST:?defina WHATSQR_SQL_HOST, ex.: usuario@servidor}"
RELEASE="${WHATSQR_SQL_RELEASE:?defina WHATSQR_SQL_RELEASE}"
CONVERSA="${1:?informe o id da conversa}"
JANELA="${2:-600}"

if [[ ! "$CONVERSA" =~ ^[0-9]+$ ]]; then
  echo "RECUSADO: o id da conversa precisa ser um numero: $CONVERSA" >&2
  exit 1
fi

# As credenciais sao lidas no servidor, do arquivo de ambiente do servidor. Nunca viajam
# por aqui, nunca entram em linha de comando e nunca aparecem em log.
echo "instante,pendentes,em_retry,enviadas"
FIM=$(( $(date +%s) + JANELA ))
while [ "$(date +%s)" -lt "$FIM" ]; do
  LINHA=$(ssh "$HOST" "
    cd '$RELEASE' || exit 1
    php8.4 bin/console doctrine:query:sql \
      \"SELECT
         SUM(status='pending') AS pendentes,
         SUM(status='retry') AS em_retry,
         SUM(status='sent') AS enviadas
       FROM meta_outbound_jobs
       WHERE JSON_EXTRACT(payload, '\\\$._inbox_conversation_id') = $CONVERSA\" \
      --env=prod 2>/dev/null | grep -oE '[0-9]+' | paste -sd, -
  ")
  echo "$(date -u +%H:%M:%S),${LINHA:-sem-resposta}"
  sleep 5
done
