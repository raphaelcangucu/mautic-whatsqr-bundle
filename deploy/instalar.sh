#!/usr/bin/env bash
# Compila aqui e manda o binario pronto. O servidor nao tem Go, e e melhor assim:
# nada de toolchain para manter numa maquina que atende cliente.
#
#   export WHATSQR_HOST=usuario@servidor
#   ./deploy/instalar.sh
#
# O host vem do ambiente, sem valor padrao: este repositorio e publico, e um
# endereco de producao nao deve viajar dentro dele.
set -euo pipefail

HOST="${WHATSQR_HOST:?defina WHATSQR_HOST, ex.: usuario@servidor}"
DEST="${WHATSQR_DEST:-\$HOME/whatsqr}"

cd "$(dirname "$0")/../service"
echo "==> compilando para linux/amd64, estatico"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ../dist/whatsqr .
file ../dist/whatsqr | grep -q 'statically linked' || { echo "RECUSADO: o binario nao ficou estatico"; exit 1; }

echo "==> enviando"
ssh "$HOST" "mkdir -p $DEST && mkdir -p \$HOME/.config/systemd/user"
scp -q ../dist/whatsqr "$HOST:$DEST/whatsqr.novo"
scp -q ../deploy/whatsqr.service "$HOST:\$HOME/.config/systemd/user/whatsqr.service"

# Troca atomica: no Linux, mover por cima de um binario em uso funciona — o processo
# antigo continua com o inode antigo ate reiniciar.
echo "==> trocando e reiniciando"
ssh "$HOST" "
  set -e
  mv $DEST/whatsqr.novo $DEST/whatsqr
  chmod 700 $DEST/whatsqr
  systemctl --user daemon-reload
  systemctl --user enable whatsqr >/dev/null 2>&1 || true
  if [ -f $DEST/whatsqr.json ]; then systemctl --user restart whatsqr; else
    echo 'AVISO: $DEST/whatsqr.json nao existe — servico instalado mas nao iniciado'
  fi
"
echo "==> pronto"
