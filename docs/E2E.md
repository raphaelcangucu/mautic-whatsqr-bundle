# Ponta a ponta — o que está provado e o que espera um chip

Canal de WhatsApp por QR Code (**não homologado**) para Mautic 7.

## Estado em 19/09/2026

Os E2E do plano 2 provaram o serviço em Go sozinho. Os deste arquivo provam o
Mautic incluído. Cinco dos seis precisam de um número pareado de verdade, e o
pareamento está bloqueado: o chip **+55 31 7505-3794** foi reativado no WhatsApp
Business em 18/09, e uma reativação recente bloqueia o vínculo de dispositivo
companheiro por algumas horas. O número precisa ser usado normalmente antes de
tentar de novo.

| # | O que prova | Situação |
|---|---|---|
| 1 | Parear pela tela de Conexões; a tela vira "conectado" sozinha | **espera o chip** |
| 2 | Mensagem de outro celular cai na caixa com o selo `QR`, e o push chega | **espera o chip** |
| 3 | Responder pela caixa; a bolha aparece na hora e a mensagem chega | **espera o chip** |
| 4 | Modo avião: três respostas dizem "na fila", saem na ordem, e **quanto tempo levam** | **espera o chip** |
| 5 | Foto vira mensagem não suportada com texto, sem sumir | **espera o chip** |
| 6 | **Opt-out recusado num número QR** | **provado, 19/09/2026** |

## E2E 6 — provado sem WhatsApp nenhum, de propósito

`MauticInboxBundle/Tests/Functional/QrChannelRespectsOptOutTest.php`, na release
de provas contra o banco descartável. Roda sem sessão pareada porque a recusa
acontece **antes** de o remetente escolher o transporte.

É a garantia que impede o canal não homologado de ser o único que ignora quem
pediu para sair. A checagem de consentimento mora no remetente do conector, não
na borda; um dos desenhos considerados dava ao plugin o próprio remetente, e
teria levado exatamente a isso.

Conferido pelos dois lados: desligar a checagem só para asset QR deixa o teste
vermelho.

**A mensagem da exceção faz parte da afirmação.** Sem ela o teste passava mesmo
com o canal pulando o consentimento inteiro, porque alguma coisa mais adiante
— sessão não configurada, transporte ausente — também estoura `DomainException`.

### O que este E2E encontrou de quebrado

A primeira versão passava **pela exceção errada**: a janela de 24 horas do WABA
estava sendo cobrada do canal por QR e disparava antes do opt-out.

Isso fechava o canal nas duas situações que são o uso normal dele — começar uma
conversa, e responder depois de um dia parado — e a saída que a mensagem
oferecia, "use um template aprovado", **não existe neste canal**. O atendente
leria uma instrução impossível de seguir e a conexão pareceria quebrada.

Corrigido no conector (`OutboundPolicy`), com os limites de anti-spam mantidos
de propósito: um número não homologado é justamente onde disparar em massa
termina em banimento.

## Quando o chip liberar

1. Abrir a sessão pela tela de Conexões, não por `curl` — é a tela que precisa
   ser provada.
2. E2E 4 é o mais importante, e **anotar o tempo real** faz parte dele. A ordem
   por conversa foi resolvida dentro do `findDue()`, e isso tem um custo
   conhecido: duas mensagens vencidas da mesma conversa não saem no mesmo lote.
   Com varredura de minuto em minuto, três respostas represadas gotejam por três
   minutos. Com três pode ser irrelevante; com quinze não é. **Medir antes de
   decidir se vale mexer.**
3. Escrever aqui o resultado de cada um, com data e número usado.
