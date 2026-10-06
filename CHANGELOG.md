# Changelog

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
