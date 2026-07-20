# Changelog

All notable changes to listnr are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
listnr is pre-1.0, so minor versions may include breaking changes; see the
upgrade notes on each release.

## [Unreleased]

## [0.2.0] - 2026-07-20

Six features, a database migration, and HTTP caching across every ActivityPub
document.

### Upgrading

**This release applies schema migration 2 and cannot be rolled back in
place.** The first start under 0.2.0 adds a nullable `posts.deleted_at`
column. An older binary then refuses to open the database:

```
database schema version 2 is newer than supported version 1
```

Take a backup (`listnr export`) before upgrading. Downgrading means restoring
that archive, not reinstalling the old binary.

The migration is additive — no table is dropped or rewritten, and existing
posts migrate in place with a NULL deletion timestamp.

### Added

- **Post deletion.** `listnr posts list` and `listnr posts delete <id>`
  withdraw a published post: the deletion timestamp and one ActivityPub
  `Delete` per follower inbox commit in a single transaction. Repeating a
  deletion is a no-op rather than a second `Delete`. A withdrawn post answers
  `410 Gone` with a `Tombstone` to fediverse software and a plain `410` to
  browsers, leaves the outbox, and reports empty `/api/interactions` results.
  Its stored replies and likes are kept for moderation.
- **Actor profile updates.** `listnr actor publish` announces the daemon's
  current actor document to followers as an `Update` carrying the full
  representation. Deduplicated by a SHA-256 fingerprint of the document, so an
  unchanged profile queues nothing. Never runs automatically.
- **Delivery queue administration.** `listnr deliveries list`, `retry`,
  `retry-failed`, and `delete` expose and recover the outbound queue. Listings
  carry each activity's type and id but never its JSON payload. Only failed
  rows can be retried and only terminal rows deleted; pending rows are refused,
  because the worker may already be sending them.
- **Actor migration.** `listnr actor move --to <actor-url> --yes` publishes an
  ActivityPub `Move`. The target is dereferenced first and must name this actor
  in its own `alsoKnownAs`. See "Known limitations" below.
- **NodeInfo.** `GET /.well-known/nodeinfo` and `GET /nodeinfo/2.1` publish the
  software name and version, `activitypub`, closed registrations, one local
  user, and the active post count. No follower or reply data is disclosed.
- **HTTP validators on ActivityPub documents.** `/actor`, `/posts/{id}`,
  `/outbox` and its pages, and `/followers` now send a strong `ETag` over the
  exact response bytes plus `Cache-Control: public, max-age=0,
  must-revalidate`, so an unchanged repeat fetch costs a bodyless `304`.
  `/actor` and `/posts/{id}` also send `Vary: Accept`.

### Changed

- `PostCount` — and therefore the outbox `totalItems`, its pages, and the
  `/admin/stats` post count — now means *active* federated posts and excludes
  withdrawn ones. `TotalPostCount` remains the all-time ingestion count used to
  detect a first run.
- After a published `Move`, feed polling performs no fetch and publishes
  nothing, `listnr refresh` returns `409`, and inbound `Follow` requests are
  acknowledged but ignored. `Undo` and `Delete` continue to work so existing
  followers can leave.
- The `/api/interactions` endpoint moved onto the shared cache helper. Its
  ETags, Cloudflare cache headers, and CORS behavior are unchanged.

### Fixed

- `Vary` headers are now appended rather than overwritten. The previous
  single-line read could drop an existing `Origin`, which would let a shared
  cache serve one origin's response to another.

### Known limitations

- **Actor migration is not verified end-to-end.** The `Move` payload follows
  the Activity Streams definition and Mastodon's documented migration
  behaviour, and is covered by automated tests, but it has not been exercised
  against a live Mastodon server. Treat it as unproven until it has been.

## [0.1.3] - 2026-07-19

### Added

- Cross-platform release builds via GitHub Actions.

## [0.1.2] - 2026-07-19

### Added

- Portable instance backup and restore: `listnr export` and `listnr import`,
  with a versioned archive carrying the database, key, config, and a manifest
  of checksums and identity metadata.

## [0.1.1] - 2026-07-19

### Changed

- Smaller release binaries.

## [0.1.0] - 2026-07-19

First tagged release.

### Added

- Single-actor ActivityPub server: WebFinger, actor document, inbox with HTTP
  signature verification, outbox, followers collection, and dereferenceable
  `Note` objects.
- RSS/Atom feed poller publishing `Create` and `Update` activities, with
  first-run backfill that is never fanned out.
- Durable outbound delivery queue with retry and backoff.
- Interactions API for blog widgets, with ETag revalidation and
  Cloudflare-oriented cache headers.
- Admin API and CLI: replies, blocks, followers, stats, and manual refresh.
- Release and schema version tracking.

[Unreleased]: https://github.com/vrypan/listnr/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/vrypan/listnr/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/vrypan/listnr/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/vrypan/listnr/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/vrypan/listnr/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/vrypan/listnr/releases/tag/v0.1.0
