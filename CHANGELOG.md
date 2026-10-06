# Changelog

## 0.2.1 - 2026-10-06

- Publica o plugin PHP e o serviço Go que foram pareados e validados no Inbox.
- Acompanha QR, conexão e reconexão por SSE autenticado, com sessão PHP liberada, renovação limitada e limpeza ao navegar.
- Separa o SSE dos workers normais do Mautic com exemplos de pool FPM e Nginx.
- Resolve o destinatário canônico pelo WhatsApp antes de enviar, incluindo a variante brasileira de oito dígitos confirmada pelo servidor.
- Fixa o Whatsmeow em `6bb48c0f1ff0`, mantém credenciais no reinício e protege contas conectadas contra renovação indevida do QR.
- Documenta dependências Meta 0.14.2 e Inbox 1.4.1, validações reais e limites de mídia, recibos e fila de webhooks.
