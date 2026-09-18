# WhatsApp por QR Code — desenho da etapa 1

**Data:** 2026-09-18
**Etapa:** 1 de 2. Esta entrega o número por QR Code na caixa de atendimento. A etapa 2, o disparo por campanha, tem desenho próprio e depende desta estar de pé.

## O que estamos construindo

Um número comum de WhatsApp, pareado por QR Code, atendido pela mesma caixa que já atende os canais oficiais da Meta. O atendente não deveria precisar saber por qual caminho a mensagem chegou — o selo no avatar diz, e é só isso que muda para ele.

Três peças:

1. **Um serviço em Go** que mantém as sessões vivas com a biblioteca [whatsmeow](https://github.com/tulir/whatsmeow) e expõe HTTP para o Mautic.
2. **O plugin `MauticWhatsQrBundle`**, que fala com esse serviço e escreve nas tabelas do `MauticMetaBundle`.
3. **Uma costura no `MauticMetaBundle`**: `WhatsAppSender` vira interface, com duas implementações escolhidas pelo tipo do asset.

## Por que WhatsMeow, e não WPPConnect ou Baileys

Os três resolvem o mesmo problema; a diferença é o custo por número e o que quebra quando a Meta mexe em algo.

| | Memória por sessão | Como fala com o WhatsApp |
|---|---|---|
| WPPConnect | Chromium inteiro, ~300–500 MB | Automatiza o WhatsApp Web |
| Baileys | ~60–100 MB de heap do Node | WebSocket, protocolo direto |
| **WhatsMeow** | **~20–40 MB, binário único** | WebSocket, protocolo direto |

O WPPConnect depende do DOM e do JavaScript interno do WhatsApp Web: quando a Meta muda a interface, ele quebra para todos ao mesmo tempo, sem aviso. Os outros dois só quebram quando o protocolo multi-device muda, o que é raro.

Entre Baileys e WhatsMeow, pesou a memória — a diferença entre caber cinco números e caber cinquenta no mesmo servidor — e o fato de o serviço ser um binário estático, sem árvore de dependências para envelhecer. Contra, Go é língua nova nesta casa.

**O plugin se chama `MauticWhatsQrBundle`, não `MauticWhatsMeowBundle`.** O nome descreve a capacidade, não a biblioteca. Trocar o motor depois não deve renomear o plugin nem quebrar o que depende dele.

## O que este canal não é

Precisa estar escrito, porque a confusão é cara:

- **Não é homologado.** Viola os termos do WhatsApp. O número pode ser banido, e o desenho assume que um dia será.
- **Não tem template homologado nem envio ativo em massa.** Isso é do WABA.
- **Não substitui os canais oficiais.** Convive com eles na mesma caixa.

## Arquitetura

### O serviço em Go

Processo único, `systemd` no mesmo servidor do Mautic, escutando em `127.0.0.1` — **nunca exposto à internet**. Até cinco sessões, uso interno.

Estado em SQLite ao lado do binário. É o que o whatsmeow já usa e é o que faz o serviço reiniciar sem perder pareamento. A sessão guardada é material sensível: quem tem o arquivo fala pelo número. Permissão `0600`, dono do serviço, fora de qualquer diretório servido pelo nginx.

**API:**

| Rota | Faz |
|---|---|
| `POST /sessions` | Abre uma sessão e devolve `{id, status: "pairing", qr}` |
| `GET /sessions/{id}` | Estado atual: `pairing` \| `connected` \| `reconnecting` \| `logged_out` \| `failed`, com `qr` renovado enquanto parear |
| `DELETE /sessions/{id}` | Desconecta e apaga a sessão |
| `POST /sessions/{id}/messages` | Envia. Recebe `{to, text, request_id}`, devolve `{message_id}` |
| `GET /health` | Estado de todas as sessões, para o monitoramento |

Autenticação por token compartilhado em cabeçalho, além do bind local. Os dois, não um: o bind local cai se alguém mudar a configuração, e o token sozinho não protege de outro processo no mesmo servidor.

**Webhook para o Mautic:** `POST {mautic}/whatsqr/webhook`, assinado com HMAC-SHA256 sobre o corpo, com o segredo compartilhado. O plugin **recusa corpo sem assinatura válida** — esta rota é pública e precisa ser, porque o serviço a chama de fora do ciclo de requisição do Mautic.

Eventos: `message` (entrada), `status` (entrega/leitura), `session` (mudança de estado da sessão). O serviço repete com recuo exponencial até o Mautic responder 200, e guarda em disco o que não entregou. Uma mensagem que chega duas vezes é reconhecida pelo id do WhatsApp e ignorada na segunda.

### O plugin

**Asset.** Um número por QR Code é um `MetaAsset` de tipo novo, `whatsapp_qr_session`, que o `AssetType` do Meta bundle ganha como caso e mapeia para `Channel::WhatsApp`. A partir daí o resto do Mautic não distingue: a caixa, o histórico, a busca e o contato funcionam sem mudança.

**Conexão.** Um `MetaConnection` guarda o id da sessão no serviço e o token, pelo `EncryptionHelper` que o Mautic já usa. Não guardamos credencial do WhatsApp — ela vive no SQLite do serviço.

**Entrada.** O webhook normaliza e grava `MetaConversation` + `MetaMessage` com o mesmo `PhoneNormalizer` dos canais oficiais. Dali em diante o token de versão do SSE muda sozinho, o poll dispara, o push sai. Zero código novo na caixa.

**Saída.** É onde mora a única mudança no Meta bundle.

## A costura

Hoje o `OutboundOperationExecutor` tem um `match` sobre o nome da operação, e `whatsapp_text` vai direto para a classe concreta `WhatsAppSender`, que fala Graph.

**A mudança:** `WhatsAppSender` vira `WhatsAppSenderInterface`. `GraphWhatsAppSender` é o que existe hoje, renomeado. `QrWhatsAppSender` vem do plugin novo. Um `WhatsAppSenderResolver` escolhe pelo `AssetType` do job.

O `match` do executor **não muda**. A caixa **não muda**. O plugin novo pode ser instalado e desinstalado sem editar o Meta bundle — é a razão de ter escolhido este caminho e não acrescentar operações novas ao `match` ou enfiar um `if` de transporte dentro do `WhatsAppSender`.

**O custo é real e precisa ser dito:** o `OutboundOperationExecutor` e o `WhatsAppSender` estão escritos num estilo comprimido, com linhas longas, e atendem três canais em produção. Extrair a interface vai dar um diff maior do que a mudança conceitual sugere. Esta parte vai primeiro, com teste, e sem nenhuma mudança de comportamento — o Graph continua fazendo exatamente o que faz hoje.

## A sessão vai cair

Não é exceção, é rotina. A Meta derruba, o aparelho fica sem internet, alguém desconecta pelo celular, catorze dias fora do ar desfazem o pareamento sozinhos.

**O que acontece quando cai:**

1. O serviço detecta e tenta reconectar sozinho, com recuo. Publica `session` com `reconnecting`.
2. O plugin marca o asset e a caixa mostra a faixa na conversa.
3. **O compositor continua aberto.** Travar ali só transfere a espera para o cliente.
4. O que o atendente escreve vira `MetaOutboundJob` e **fica na fila**. Sai sozinho quando a sessão voltar.
5. Se o serviço concluir que não volta — `logged_out` —, o asset pede pareamento de novo e a tela de Conexões mostra quantas respostas estão paradas.

**Dois estados que precisam parecer diferentes na tela:**

- **na fila** — o WhatsApp ainda não viu; sai quando voltar
- **não saiu** — o WhatsApp recusou; tentar de novo não resolve

Misturar os dois é o jeito mais rápido de um atendente achar que mandou e não ter mandado. É a mesma distinção que a bolha pendente da caixa já faz entre falha de rede e recusa do canal, e usa o mesmo vocabulário visual.

**Um limite da fila.** Uma mensagem parada há muito tempo deixa de fazer sentido — responder "já vou verificar" seis horas depois é pior que não responder. Jobs na fila de um número desconectado expiram em **duas horas** e passam a *não saiu*, com o motivo escrito. O número é discutível; o comportamento de expirar não é.

## Telas

Desenhadas e aprovadas em <https://claude.ai/artifact/XKQWTWepYCAwqe38GA8tKi>.

**Conexões** — lista dos números, com situação, quantas respostas estão na fila, e quando foi a última mensagem. A coluna da fila não é decoração: é como alguém descobre que três clientes estão esperando.

**Parear** — um componente, três estados. Esperando (com o QR e o contador de renovação), conectou, não deu. O estado "não deu" distingue *expirou* de *o WhatsApp recusou*, e só o primeiro oferece tentar de novo.

**A caixa** — sem tela nova. O número por QR Code entra na mesma lista, com selo no avatar e a linha de contexto dizendo `WhatsApp · QR · <nome do número>`.

**Queda** — a faixa na conversa, as bolhas na fila, e o compositor aberto dizendo para onde o texto vai.

## Testes

**No serviço em Go:** testes de unidade sobre a máquina de estados da sessão (as transições entre `pairing`, `connected`, `reconnecting`, `logged_out`) e sobre a fila de reenvio do webhook. A biblioteca whatsmeow entra por interface, para o teste não precisar de um WhatsApp de verdade.

**No plugin:** teste funcional do webhook — assinatura válida grava, assinatura inválida recusa, mensagem repetida não duplica. Teste do `WhatsAppSenderResolver` provando que um asset Graph vai para o Graph e um asset QR vai para o serviço. Teste da expiração da fila.

**No Meta bundle:** a extração da interface é refactor puro. O teste é que o comportamento do Graph não mudou — os testes que já existem precisam continuar verdes sem edição, e é esse o critério.

**O que nenhum teste automatizado pega**, e precisa ser feito no aparelho: parear de verdade, mandar e receber, desconectar o celular da internet e ver a fila segurar, reconectar e ver a fila sair.

## Fora do escopo desta etapa

- **Mídia** — foto, áudio, documento. Entra depois; o desenho do serviço já prevê a rota, mas a implementação não está aqui.
- **Grupos.** O whatsmeow suporta; a caixa não tem conceito de conversa com muitos participantes.
- **Disparo por campanha.** Etapa 2.
- **Vários clientes no mesmo serviço.** Até cinco números, todos de vocês. Isolamento entre clientes mudaria o desenho e não é preciso agora.

## Riscos, sem eufemismo

**O número pode ser banido.** É o risco central e não tem mitigação técnica completa. O que dá para fazer: não usar este canal para disparo em massa na etapa 1, respeitar cadência humana, e manter o histórico no Mautic para que perder o número não signifique perder o atendimento.

**Go é língua nova aqui.** O serviço é pequeno e bem delimitado, mas "pequeno numa língua que ninguém lê" é dívida. Mitigação: manter o serviço burro — ele não decide nada de negócio, só mantém a sessão e repassa.

**O arquivo de sessão fala pelo número.** Quem copiar o SQLite se passa pelo seu WhatsApp. Permissão restrita, fora de diretório servido, e no backup ele é tratado como credencial.
