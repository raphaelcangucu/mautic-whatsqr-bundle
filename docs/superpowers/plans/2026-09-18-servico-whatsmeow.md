# O serviço WhatsMeow — plano de implementação

> **Para quem for executar:** SUB-SKILL OBRIGATÓRIA — use `superpowers:subagent-driven-development` ou `superpowers:executing-plans`.

**Objetivo:** um serviço em Go que mantém sessões do WhatsApp vivas por QR Code, instalado e rodando no servidor de produção, com teste de ponta a ponta feito num aparelho de verdade.

**Arquitetura:** binário estático, compilado na máquina de quem desenvolve e enviado pronto. Roda como serviço do `systemd --user` do usuário `forge`, escutando só em `127.0.0.1`. Estado em SQLite ao lado do binário.

**Pilha:** Go 1.23, `go.mau.fi/whatsmeow`, `modernc.org/sqlite`. Repositório: `/Users/raphaelcangucu/projects/mautic-whatsqr-bundle`, pasta `service/`.

**Plano 2 de 3.** O 1 são as costuras no Meta bundle; o 3 é o plugin. Este entrega sozinho: ao fim dele, um número real está pareado e trocando mensagens pela linha de comando, sem o Mautic saber.

---

## O que foi verificado no servidor, e não suposto

| | Achado | O que muda |
|---|---|---|
| Arquitetura | `x86_64` | Compilar para `linux/amd64` |
| Go no servidor | **não instalado** | Compilar aqui e mandar o binário; nada de toolchain em produção |
| `systemd --user` | **rodando** | Serviço sem sudo |
| `Linger` | **`no`** | Precisa de **um** comando com sudo, ou o serviço morre quando a sessão SSH fecha |
| Supervisor | existe, mas `supervisorctl` exige sudo | Não usar; o systemd de usuário resolve |

---

## Uma armadilha que custa um dia se você descobrir sozinho

O `whatsmeow` guarda a sessão num `sqlstore`, e o driver SQLite mais comum em Go (`mattn/go-sqlite3`) **exige CGO**. Com CGO ligado, compilar no macOS para Linux precisa de um cross-compiler — e o binário deixa de ser estático, que é metade do motivo de termos escolhido Go.

**Use `modernc.org/sqlite`**, que é SQLite traduzido para Go puro. Aí `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` produz um binário estático sem nenhuma ferramenta extra.

MySQL não serve: o `sqlstore` do whatsmeow suporta SQLite e Postgres, e vocês têm MySQL.

---

## Estrutura de arquivos

| Arquivo | Responsabilidade |
|---|---|
| `service/main.go` | Sobe o HTTP, lê a configuração, liga as sessões salvas |
| `service/session/manager.go` | O conjunto de sessões: abrir, estado, fechar, enviar |
| `service/session/state.go` | A máquina de estados, sem nada de rede |
| `service/session/whatsapp.go` | A interface por onde o whatsmeow entra — e sai, no teste |
| `service/webhook/sender.go` | Assina, envia, tenta de novo com recuo |
| `service/api/routes.go` | As cinco rotas e o token |
| `service/config.go` | Configuração em arquivo, permissão `0600` |
| `deploy/whatsqr.service` | A unidade do systemd |
| `deploy/instalar.sh` | Compila, envia, instala, liga |

---

## Tarefa 1: A máquina de estados, sem rede nenhuma

**Arquivos:** criar `service/session/state.go`, `service/session/state_test.go`

Esta é a parte que tem regra de verdade, e é a única testável sem WhatsApp. Faça primeiro: o resto é encanamento em volta dela.

- [ ] **Passo 1: Escrever os testes**

```go
func TestPairingBecomesConnectedOnScan(t *testing.T)
func TestConnectedBecomesReconnectingOnDrop(t *testing.T)
func TestReconnectingBecomesLoggedOutAfterLogout(t *testing.T)
func TestLoggedOutNeedsPairingAgain(t *testing.T)
// O que impede o pior caso do desenho: o numero volta, mas e outro chip.
func TestReconnectWithADifferentJidIsRefused(t *testing.T)
```

O último merece o cuidado: sem ele, o número é banido na sexta, alguém pareia outro chip na segunda reaproveitando a mesma sessão, e as respostas que estavam na fila saem pelo número novo. O cliente recebe "conforme combinamos" de um número que nunca viu.

- [ ] **Passo 2: `go test ./service/session/` e ver falhar**
- [ ] **Passo 3: Implementar as transições** — nenhuma chamada de rede neste arquivo
- [ ] **Passo 4: Verde. Commit.**

---

## Tarefa 2: O whatsmeow entra por interface

**Arquivos:** criar `service/session/whatsapp.go`, `service/session/manager.go`, `service/session/manager_test.go`

- [ ] **Passo 1: Declarar a interface antes de usar a biblioteca**

```go
type Cliente interface {
    Conectar(ctx context.Context) (<-chan Evento, error)
    QrAtual() string
    Jid() string
    EnviarTexto(ctx context.Context, para string, texto string) (string, error)
    Desconectar()
}
```

Escrever isto **antes** de abrir a documentação do whatsmeow é o que impede o formato da biblioteca de vazar para dentro do serviço — e é o que vai permitir trocar por Baileys um dia sem reescrever o gerente.

- [ ] **Passo 2: Testes do gerente com um cliente de mentira**

```go
func TestOpeningASessionReturnsAQr(t *testing.T)
func TestSendingOnADroppedSessionFails(t *testing.T)
func TestTwoSessionsDoNotShareState(t *testing.T)
```

- [ ] **Passo 3: Rodar, implementar o gerente, rodar. Commit.**
- [ ] **Passo 4: Só agora, a implementação real com o whatsmeow**

`service/session/whatsmeow.go`, implementando `Cliente`. Sem teste de unidade — é a borda.

---

## Tarefa 3: O webhook que sai

**Arquivos:** criar `service/webhook/sender.go`, `service/webhook/sender_test.go`

- [ ] **Passo 1: Testes**

```go
func TestItSignsTimestampAndBody(t *testing.T)
func TestItRetriesWithBackoffUntilTwoHundred(t *testing.T)
func TestItGivesUpAndDropsTheOldestWhenTheBufferIsFull(t *testing.T)
func TestEveryEventCarriesItsOwnId(t *testing.T)
```

O terceiro é o corte que a revisão do desenho fez: **buffer em memória, não fila em disco.** O Mautic já tem fila durável com dedupe; construir outra em Go é duplicar infraestrutura numa língua que ninguém aqui lê. Se o Mautic ficar fora do ar mais que o buffer, o que se perde é um evento que o próprio WhatsApp ainda tem.

O quarto existe porque o dedupe do Mautic é por chave, e `status` e `session` não têm id de mensagem para usar.

- [ ] **Passo 2: Rodar, implementar, rodar. Commit.**

---

## Tarefa 4: As cinco rotas

**Arquivos:** criar `service/api/routes.go`, `service/api/routes_test.go`, `service/config.go`, `service/main.go`

- [ ] **Passo 1: Testes**

```go
func TestEveryRouteRequiresTheToken(t *testing.T)
func TestItOnlyListensOnLoopback(t *testing.T)
func TestSendReturnsTheMessageId(t *testing.T)
```

- [ ] **Passo 2: Rodar, implementar, rodar.**
- [ ] **Passo 3: A configuração**

Arquivo com o token do serviço, o segredo do webhook e a URL do Mautic. **O serviço recusa subir se a permissão não for `0600`** — esse arquivo dá o mesmo poder que o SQLite da sessão, e a primeira versão do desenho esqueceu dele.

- [ ] **Passo 4: Commit.**

---

## Tarefa 5: Instalar no servidor de verdade

**Arquivos:** criar `deploy/whatsqr.service`, `deploy/instalar.sh`

- [ ] **Passo 1: A unidade do systemd**

```ini
[Unit]
Description=WhatsQR — sessoes de WhatsApp por QR Code
After=network-online.target

[Service]
Type=simple
WorkingDirectory=%h/whatsqr
ExecStart=%h/whatsqr/whatsqr
Restart=always
RestartSec=5
UMask=0077

[Install]
WantedBy=default.target
```

`UMask=0077` é o que faz o SQLite da sessão nascer inacessível para os outros, sem depender de ninguém lembrar do `chmod`.

- [ ] **Passo 2: O script de instalação**

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/whatsqr ./service
rsync dist/whatsqr "$HOST:~/whatsqr/whatsqr.novo"
# troca atomica: mv por cima do binario em uso funciona no Linux
ssh "$HOST" "mv ~/whatsqr/whatsqr.novo ~/whatsqr/whatsqr && systemctl --user restart whatsqr"
```

O host vem do ambiente, sem valor padrão — mesma regra do `bin/dev-test.sh` do inbox, e pelo mesmo motivo: este repositório é público e um endereço de produção não viaja dentro dele.

- [ ] **Passo 3: O comando com sudo, que é do usuário**

```bash
sudo loginctl enable-linger forge
```

**Sem isto o serviço morre quando a última sessão SSH do `forge` fechar** — ou seja, funciona no teste e cai de madrugada. É o único sudo deste plano.

- [ ] **Passo 4: Ligar e conferir**

```bash
systemctl --user enable --now whatsqr
systemctl --user status whatsqr
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8088/health
```

- [ ] **Passo 5: Conferir que a porta NÃO responde de fora**

```bash
curl --max-time 5 http://78.46.212.196:8088/health   # precisa falhar
```

Um teste que só confirma que funciona não vale nada aqui: o que importa é que **não** funciona de fora.

- [ ] **Passo 6: Commit.**

---

## Tarefa 6: Ponta a ponta, num número de verdade

Nada disto é automatizável por inteiro — pareamento exige um polegar humano. Os passos automatizados estão marcados; os manuais dizem o que pedir.

**Antes de começar:** use um chip que possa ser banido. Este canal não é homologado.

- [ ] **E2E 1 — Parear**

*Automatizado:* `POST /sessions`, receber o QR, renderizar no terminal.
*Humano:* escanear com o aparelho.
*Conferir:* `/health` diz `connected`, e o JID gravado é o do número esperado.

- [ ] **E2E 2 — Receber**

*Humano:* mandar "teste de entrada" de outro celular para o número pareado.
*Conferir:* o webhook chegou, com assinatura válida, e o corpo traz o texto e o remetente.

- [ ] **E2E 3 — Enviar**

*Automatizado:* `POST /sessions/{id}/messages`.
*Conferir:* chegou no outro aparelho, e a resposta trouxe um `message_id` não vazio.

- [ ] **E2E 4 — A queda, que é o que o desenho todo existe para tratar**

*Humano:* pôr o aparelho pareado em modo avião.
*Conferir:* o estado vira `reconnecting` sozinho, sem ninguém pedir.
*Automatizado:* tentar enviar → precisa falhar com a falha **temporária**, não com uma permanente.
*Humano:* tirar do modo avião.
*Conferir:* volta a `connected` sozinho.

- [ ] **E2E 5 — Voltar como outro chip**

*Humano:* desconectar pelo WhatsApp do aparelho e parear um **chip diferente** na mesma sessão.
*Conferir:* **recusado**, com o motivo dizendo que o JID mudou.

Este é o único E2E cuja falha é silenciosa e cara: se passar errado, ninguém percebe até um cliente receber mensagem de um número estranho.

- [ ] **E2E 6 — Sobreviver ao reinício**

*Automatizado:* `systemctl --user restart whatsqr`.
*Conferir:* volta a `connected` **sem** pedir QR de novo. Se pedir, o SQLite não está sendo lido — e aí cada reinício derruba o atendimento.

- [ ] **Passo final: escrever o resultado de cada um no repositório**, com data e número usado. Um E2E que ninguém registrou não aconteceu.

---

## Critério de pronto

1. `go test ./...` verde.
2. O serviço está rodando no servidor sob `systemd --user`, e sobreviveu a um reinício da máquina.
3. `curl` de fora não alcança a porta.
4. Os seis E2E foram executados **num número real**, e o resultado de cada um está escrito.
5. Nenhuma linha de PHP foi tocada neste plano.
