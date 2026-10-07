# Changelog

## 0.3.2 - 2026-10-07

- Sincroniza respostas privadas enviadas pelo celular ou outro dispositivo, excluindo grupos, broadcasts e newsletters.
- Solicita histórico disponível usando âncoras reais de conversas privadas, com limites e intervalo mínimo entre solicitações.
- Persiste eventos históricos em fila privada e limitada; mantém datas originais, deduplicação por conta e prioridade das mensagens atuais.
- Suprime ecos de mensagens enviadas pelo Inbox e resolve identificadores privados com cache de telefones e contatos, sem uma consulta por mensagem.
- Adiciona solicitação explícita de histórico à interface, protegida por permissão e CSRF; não apaga nem refaz pareamentos automaticamente.
- Preserva validação estrita de anexos e remove parâmetros de URLs de mídia dos logs.
- Recomenda Inbox 1.5.2 para exibir importações sem notificações de mensagens novas.
- Validação real recuperou duas mensagens antigas de uma conversa privada, incluindo uma resposta do celular. A solicitação geral de histórico foi recusada pelo telefone; a recuperação de todas as conversas antigas e um novo envio via Android USB não foram comprovados.

## 0.3.1 - 2026-10-07

- Adiciona **Editar nome** e **Nova conexão** à tela de conexões WhatsApp QR.
- Renomeia somente a identificação interna, preservando sessão, telefone, histórico e credenciais.
- Cria novas contas usando um servidor já configurado, com ID e segredo exclusivos; inicia o pareamento apenas por POST explícito.
- Provisiona sessões no serviço Whatsmeow por API autenticada e aditiva, com registro privado persistente e proteção contra substituição de segredos existentes.
- Valida permissões, CSRF, nome e servidor; traduz formulários e mensagens de validação em português e inglês.
- Adapta os formulários a telas menores. Validação: 107 testes unitários / 527 asserções, quatro testes JavaScript e testes Go com detector de concorrência.

## 0.3.0 - 2026-10-06

- Exige varredura ClamAV para anexos, com bloqueio em caso de erro/indisponibilidade; confere SHA-256 também no cache e o MIME real/declaração/extensão.
- Recusa formatos perigosos/desconhecidos, documentos ativos e arquivos abusivos; limita imagens/quadros/pixels, expansão OOXML, ranges e streaming. Documentos são downloads autenticados.
- Atualiza `golang.org/x/image` para 0.45.0 e fixa a preferência pelo toolchain Go 1.26.8 após auditoria de vulnerabilidades WebP.

- Recebe imagens, vídeos, áudios, documentos e figurinhas pelo Whatsmeow, preservando legenda e nome do arquivo no Inbox.
- Guarda referências de mídia em disco privado, sem chaves ou URLs temporárias no navegador/banco; baixa sob demanda com validação, cache persistente e até duas buscas simultâneas.
- Usa streaming e byte ranges para previews de mídia, com limite de 32 MiB por arquivo e 2 GiB de armazenamento privado. Não arquiva mídia de visualização única.
- Recupera fotos de perfil dos contatos WhatsApp por QR através de uma rota autenticada do Mautic.
- Adiciona cache positivo/negativo, coalescência, limite de memória e concorrência; libera a sessão PHP antes da busca.
- Mantém as iniciais quando a foto está ausente ou restrita e preserva os canais oficiais.

## 0.2.1 - 2026-10-06

- Publica o plugin PHP e o serviço Go que foram pareados e validados no Inbox.
- Acompanha QR, conexão e reconexão por SSE autenticado, com sessão PHP liberada, renovação limitada e limpeza ao navegar.
- Separa o SSE dos workers normais do Mautic com exemplos de pool FPM e Nginx.
- Resolve o destinatário canônico pelo WhatsApp antes de enviar, incluindo a variante brasileira de oito dígitos confirmada pelo servidor.
- Fixa o Whatsmeow em `6bb48c0f1ff0`, mantém credenciais no reinício e protege contas conectadas contra renovação indevida do QR.
- Documenta dependências Meta 0.14.2 e Inbox 1.4.1, validações reais e limites de mídia, recibos e fila de webhooks.
