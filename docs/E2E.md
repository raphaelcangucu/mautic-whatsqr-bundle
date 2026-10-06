# Ponta a ponta — o que está provado e o que espera um chip

Canal de WhatsApp por QR Code (**não homologado**) para Mautic 7.

## Estado em 06/10/2026

O pareamento foi confirmado pelo operador no celular e pelo servidor às
02:59:48 UTC: `PairSuccess`, autenticação às 02:59:49 e `Connected` às 02:59:52.
Uma credencial está salva no SQLite e o Mautic registrou status, JID e telefone.
A página de pareamento mostra conectado. O erro de rota do botão de Inbox foi
corrigido e validado no navegador.

Antes da atualização, a tentativa permaneceu em pareamento e expirou sem
`PairSuccess`. O serviço foi atualizado de `2e338d0ee73d` para `6bb48c0f1ff0`,
com versão Web 2.3000.1049294120. O novo código conectou. Isso comprova o resultado
da atualização e nova tentativa, mas não isola qual mudança resolveu a falha.
Não há evidência atual para atribuir a falha anterior à reativação do chip.

| Prova | Resultado |
|---|---|
| Pareamento real no celular | Confirmado pelo operador |
| Autenticação e sessão persistida | Confirmadas no servidor |
| Estado e telefone no Mautic | Confirmados por consulta somente leitura |
| Página mostra conectado e abre Inbox | Validada no navegador |
| Mensagem real recebida no Inbox | Em validação |
| Resposta real pelo Inbox | Ainda não validada |
| Reinício sem novo QR | Confirmado: restauração e autenticação às 03:07:15 UTC |
| Queda prolongada e fila em ordem | Ainda não validadas nesta instalação |
| Recibos de leitura | Não implementados no plugin |

Verificações locais: 76 testes PHP do plugin (350 asserções), testes unitários
Go de API/webhook e 34 funções sem banco de sessão/estado, 346 arquivos PHP sem
erros de sintaxe. Nenhum teste de banco foi executado contra produção.

Backups verificados ficam em `/home/forge/whatsqr-backups/20261006T024148Z` e
`/home/forge/whatsqr-backups/20261006T025626Z`; incluem dump Mautic e cópia SQLite
consistente. Contêm segredos e não devem ser publicados.

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

## Próximas provas controladas

1. Abrir a sessão pela tela de Conexões, não por `curl` — é a tela que precisa
   ser provada.
2. E2E 4 é o mais importante, e **anotar o tempo real** faz parte dele. Use
   `bin/medir-fila.sh <id-da-conversa>`: ele imprime uma linha a cada cinco
   segundos com quantas respostas estão pendentes, em retry e enviadas. Está
   escrito de antemão de propósito — ninguém escreve isso com o celular na mão e
   o cronômetro correndo. A ordem
   por conversa foi resolvida dentro do `findDue()`, e isso tem um custo
   conhecido: duas mensagens vencidas da mesma conversa não saem no mesmo lote.
   Com varredura de minuto em minuto, três respostas represadas gotejam por três
   minutos. Com três pode ser irrelevante; com quinze não é. **Medir antes de
   decidir se vale mexer.**
3. Escrever aqui o resultado de cada um, com data e número usado.
