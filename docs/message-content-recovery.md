# WhatsApp message content and recovery

The paired-phone connector decodes text, contact cards, location snapshots,
polls and interactive replies. Media continues through the existing private
attachment service, including circular WhatsApp videos (`ptvMessage`). Document
and ephemeral wrappers are unwrapped by Whatsmeow before decoding. View-once
messages are identified but their body and media are never archived.

## Compatibility and safety

Signed message webhooks add optional `content_type` and `unsupported_reason`.
Older service events still work. The new fields use bounded classifications;
raw protobufs, vCards, poll encryption keys, thumbnails and media credentials do
not cross this boundary. Contact summaries include at most 20 contacts and only
bounded names and telephone values. Poll summaries include at most 12 options.
Location summaries use a validated coordinate snapshot, not live tracking.

Images, audio, video, stickers and documents retain existing account-scoped
private references, the 32 MiB size limit, MIME validation, integrity checks,
malware scanning and authenticated proxy access. This change does not bypass
those checks or expose media URLs/keys. Groups and broadcast channels remain
excluded. Summaries cannot trigger consent keywords.

The Inbox presents unrecognized QR content as a synchronization notice. Cloud
API unsupported-content handling remains unchanged. A view-once message has a
specific notice explaining why it is not archived.

## Repairing an existing incomplete message

When a retry or history event returns the original message in a supported
format, the ingestor can enrich an existing `unsupported` record. It requires
the same account, original message ID, private conversation peer and direction.
Only connector-created records qualify. Existing readable messages are never
overwritten. Invalid, oversized or unavailable media cannot repair a record.

Repair changes content and the modification marker only. It preserves the
original timestamp, delivery status, contact, conversation assignment,
unread count and lifecycle. It updates the Inbox version for SSE without
push notifications, consent changes, campaign dispatch or AI replies.

The loopback-only service exposes authenticated `POST /sessions/{id}/recover`
with one existing message anchor (`jid`, `id`, `from_me`, `timestamp`). It sends
Whatsmeow's `BuildUnavailableMessageRequest` to the primary paired phone, not a
customer message and not a full history request. A five-minute per-session
cooldown is shared with history requests. `202 requested` means the request
was accepted, not that the content was recovered. The phone may no longer
provide the original content; the Inbox then retains a truthful notice.

## Validation

Run `php Tests/Standalone/message-content.php` for pure recovery/security cases.
`Tests/Unit/Application/InboundIngestorTest.php` uses repository/connection mocks
and an autoload-only local bootstrap, without a Mautic kernel or database.

Go checks run on local temporary files with fake clients:

```sh
go test -race ./session -run 'Test(Content|Attachment|History)' -count=1
go test -race ./webhook ./media ./api -count=1
```

The session selection intentionally excludes the SQLite store tests. Never run
Mautic database tests against the production installation.
