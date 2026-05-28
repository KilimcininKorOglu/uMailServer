// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file defines ownership boundaries that clarify which package "owns"
// which kind of state. These boundaries are explicit in code, not implicit
// in worker memory.
//
// # Ownership Map
//
// The following rules define the authoritative ownership of state:
//
//  Legacy stores (still authoritative during Phase 0, gradually migrated):
//
//  | Store                | Location               | Remains authoritative for                     |
//  |----------------------|------------------------|-----------------------------------------------|
//  | Account metadata     | internal/db            | Account records, domains, aliases              |
//  | Mailbox metadata     | internal/storage       | UIDValidity, UIDNext, HighestModSeq (IMAP)   |
//  | Message blobs        | internal/storage/.msgstore | Raw MIME blobs keyed by content hash       |
//  | DAV objects          | internal/caldav, carddav | Calendar (.ics) and contact (.vcf) files   |
//  | Sieve scripts        | internal/sieve        | User sieve scripts, vacation auto-reply state |
//
//  Canonical stores (semantic-core authoritative):
//
//  | Store                | Location               | Authoritative for                            |
//  |----------------------|------------------------|-----------------------------------------------|
//  | Identity family      | internal/semcore       | MailboxId, FolderId, ItemId, ChangeKey       |
//  | Conversation lineage | internal/semcore       | ConversationId, thread ordering             |
//  | Sync state           | internal/semcore       | SyncToken, watermark, tombstone records     |
//  | Lifecycle journal    | internal/semcore       | Lifecycle events (Phase 1+)                 |
//
//  Projection adapters own their protocol-specific wire formats but must
//  translate to/from canonical semantic-core state. They do not invent
//  independent storage truth.
//
// # Migration Path
//
// Phase 0 documents the target ownership. Phase 1 implements the migration
// framework. The transition is driven by FeatureGate flags (rollout.go):
//
//  1. legacy-only mode: all reads/writes go through existing stores
//  2. canonical-writes mode: new objects written to canonical store + legacy mirror
//  3. canonical reads: reads prefer canonical store, fall back to legacy lookup
//  4. legacy shutdown: legacy stores become read-only; canonical is sole authority
//
// This file can be updated in Phase 1 when concrete migration code is written.
package semcore
