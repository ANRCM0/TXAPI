# Secret storage and recovery

Status: A3/A4 foundation accepted on 2026-10-18. Final feature head
`56a205c` and merged `master/ccbfe29` passed complete existing CI; PR #6
provides the common baseline. This accepts the configuration below, not
plaintext compatibility deployments or unimplemented S7 product capabilities. See [PLAN](../PLAN.md) and the
[foundation contract audit](foundation-contracts.md).

A02, independently accepted by PR #12 / `master/765501a`, also uses the same mounted content-key ring for
domain-separated encrypted history snapshots. Retained history headers are
included in startup key requirements; fixed TTL/deletion and snapshot quotas
are specified in [history contract](responses-history.md). This is independent
of the accepted foundation/A01 evidence below; its exact head and merged master
passed their own complete CI, recorded in [A02 acceptance](../history/implementation-s7-a02.md#2026-10-18-s7-a02-functional-acceptance).

## Supported deployment boundary

The production security configuration uses `database.credential_keys_file`
(or `GATEWAY_CREDENTIAL_KEYS_FILE`). The optional durable runtime additionally
requires `generation.keys_file` (or `GATEWAY_GENERATION_KEYS_FILE`) and strict
billing. The runtime checks the actual store's persisted encryption activation,
not just a configuration path. It does not provide public task APIs.

S7-A01 separately accepted an opt-in foreground Responses runtime through
PR #10 / `master/a925140` and its own complete push/PR/master CI.
It requires actual credential encryption plus `responses.keys_file` /
`GATEWAY_RESPONSES_KEYS_FILE`, using this same content-key format. Its stable
idempotency digest marker is separate from the durable-intent marker; retain
the digest key and every required ciphertext key across restart/restore. Both
runtimes use the same content-key file when enabled together. This extension
is not accepted by historical A3/A4 CI; see [Responses HTTP](responses-http.md)
and [the independent A01 acceptance](../history/implementation-s7-a01.md#2026-10-18-s7-a01-functional-acceptance).

HTTP-only deployments may keep the old plaintext compatibility mode by not
configuring credential encryption. Main emits a warning in that mode. This is
not the production security baseline; management response masking does not
encrypt SQLite. Activation is one-way for that database: removing the key file
or changing the stable digest key refuses startup rather than downgrading.

Use separate, access-restricted mounted secret files for credential and content
key rings. A file is at most 16 KiB and has this JSON shape (placeholders are not
usable keys):

```json
{
  "active_key": "key-2026-10",
  "keys": {"key-2026-10": "<BASE64_OF_32_RANDOM_BYTES>"},
  "digest_key": "<BASE64_OF_32_DIFFERENT_RANDOM_BYTES>"
}
```

There are 1-8 keys. IDs are 1-32 ASCII letters, digits, `_` or `-`; they are
non-secret and immutable identifiers. Each value decodes to exactly 32 bytes.
Duplicate/unknown JSON fields, trailing values and digest/content key reuse are
rejected. Generate keys with a secure random source or secret manager, not a
password, user API Key, fixed example or repository configuration. Grant read
access only to the gateway service account (Windows ACL or Unix mode/owner),
mount read-only, and keep keys outside the database volume and source tree.
Configuration contains paths only; error messages do not echo secret values.

Legacy YAML seed entries may still contain raw upstream/client keys. Database
encryption does not sanitize those configuration files, shell history or old
copies. After the one-time import, remove secret-bearing seed entries from the
runtime configuration and protect or retire their backups under the deployment's
approved policy. Seed writes themselves use the same encrypted store writer.

## Initial credential activation

1. Stop the old process and all other database writers. This application targets
   one instance with exclusive startup, not live rolling multi-instance upgrade.
2. Make a consistent, recoverable database backup after shutdown/checkpoint.
   Preserve the old binary/configuration and protect the backup as plaintext.
3. Configure the credential key-file path and start the new binary. Unified
   schema v10 adds encryption/claim columns and indexes; an independent feature
   transaction authenticates or converts credentials and pins
   `credentials.encryption_version`. This is not another schema migration runner.
4. Only after conversion and cache initialization succeed may producers start.
   All rows convert atomically; IDs, channel ownership, health generation and
   configuration update timestamps are preserved. A failed conversion does not
   save its activation marker or half-converted rows. Structural schema changes
   already applied by AutoMigrate may still remain; do not delete version markers.
5. Confirm service health and configuration publication using the existing
   authenticated management API. Do not use SQL/API output to print real keys.

Encrypted writes allocate the row ID and seal the key in the same transaction;
plaintext key bytes never become SQL insert/update parameters. The legacy
`api_key` column is empty after activation. HMAC-SHA256 fingerprints support
same-channel duplicate detection; the stable digest-key fingerprint pins this
namespace. AES-256-GCM authenticates the format/key ID plus domain-separated
credential ID and channel ID. Runtime copies decrypt only inside store and do
not expose encryption fields in JSON. Key fingerprints, IDs, states and other
scheduling metadata are not all confidential, and this is not an authenticated
financial database or defense against an administrator rewriting the ledger.

## Rotation, retention and restore

- Stop the process and make a consistent backup first. Add a new ID/key, retain
  every old key, and select the new active ID. Keep `digest_key` unchanged.
  Credential startup validates all rows and atomically re-encrypts older IDs;
  it never treats unreadable/mixed plaintext rows as empty credentials.
- After successful credential rotation, all current credential rows use the new
  ID. Older backups still require their original keys. Retire an old key only
  after the relevant backup retention/recovery requirements are satisfied.
- Generation input/output uses the same versioned AEAD envelope but separate
  domain/identity metadata. Startup checks bounded retained headers and the
  global `generation.intent_digest_version`; this checks key-ID availability,
  not full authentication of every content blob. Wrong content bytes/keys are
  detected on consumption/read, return no partial plaintext and never authorize
  replay or a free financial release.
- Content rotation writes new segments with the active ID and retains old IDs
  until expiry cleanup or explicit deletion actually removes the ciphertext.
  Expired but not yet cleaned rows still require their old key IDs at startup.
  There is no bulk content
  re-encryption or automatic KMS. Rotate before any single AES key accumulates
  2^32 AEAD envelopes (segments, credential writes and cursor tokens) across all instances/backups of its
  use; count reuse of the same bytes across domains, not only a single key ID.
- Input ciphertext is purged at TTL even for unresolved execution. Its minimal
  owner/idempotency/financial metadata and holds remain until reconciliation;
  unresolved metadata continues to consume record capacity. Settled terminal
  metadata can be removed after TTL without deleting the ledger. Global digest
  markers remain even when no content is left; do not remove them to change keys.
- Restore a compatible binary, consistent database and original key material
  together while stopped. A v9 or older binary cannot open v10. Downgrade means
  restoring the old consistent backup, not editing migration/encryption markers.
  No automatic digest-key rotation or lost-key recovery is provided.

## Limits that encryption does not solve

`secure_delete` is enabled for the configured SQLite connection, but activation
does not retroactively erase old backups, WAL frames, free pages, filesystem
snapshots, copy-on-write replicas or physical media. Do not claim secure erasure
or delete backups merely because conversion succeeded. Follow the deployment's
approved checkpoint/compaction and backup destruction procedures while stopped.

The application necessarily holds decrypted upstream keys and request/response
bytes in memory. Temporary raw copies are cleared where practical; Go strings,
AEAD schedules, heaps, dumps and OS paging are not securely erased. Protect the
process, host, logs, backups and TLS termination. Logical content quotas do not
bound physical WAL/index/backup size. Losing the key file makes encrypted data
unrecoverable; losing both keys and database defeats this at-rest protection.
