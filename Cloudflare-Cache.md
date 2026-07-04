# Caching `/api/interactions` on Cloudflare

The blog widget fetches `GET /api/interactions?url=<post-url>` on every page
load. To keep a viral post from hammering listnr's origin (a single SQLite
connection), Cloudflare should absorb those requests at its edge.

listnr already sends the right response headers:

```
Cache-Control: public, max-age=0, s-maxage=30, stale-while-revalidate=300
ETag: "<fingerprint of the response body>"
Access-Control-Allow-Origin: *
```

- `s-maxage=30` — Cloudflare's edge caches the response for 30 s. A burst
  collapses to roughly one origin fetch per 30 s window instead of one per
  visitor.
- `stale-while-revalidate=300` — after the 30 s, the edge serves the slightly
  stale copy **instantly** and refreshes it in the background, so the origin
  sees a trickle, never a spike, and no visitor waits on a revalidation.
- `max-age=0` — browsers revalidate on each load, but that revalidation is a
  cheap `304 Not Modified` served from Cloudflare's edge, and the `ETag`
  fingerprint changes the moment a reply/like/boost/hide/delete alters the
  output. So the widget stays close to live.

But headers alone are not enough: **Cloudflare does not cache `/api/*` paths by
default** (it only caches static file extensions). You must add a Cache Rule,
and you should enable Tiered Cache. Both steps are below.

---

## 1. Cache Rule (required)

Dashboard → your zone → **Caching → Cache Rules → Create rule**.

- **Rule name:** `Cache listnr interactions`
- **When incoming requests match:**
  - Field: `URI Path`
  - Operator: `starts with`
  - Value: `/api/interactions`

  (If listnr is on its own hostname, e.g. `ap.vrypan.net`, that alone is
  enough. If it shares a zone, add `AND Hostname equals ap.vrypan.net`.)
- **Then:**
  - **Cache eligibility:** `Eligible for cache`
  - **Edge TTL:** `Use cache-control header if present, bypass cache if not`
    — this makes Cloudflare honor the `s-maxage`/`stale-while-revalidate`
    above. Do **not** hard-code an Edge TTL here, or you lose the ability to
    tune it from listnr.
  - **Browser TTL:** `Respect origin TTL`.

Leave the Cache Key at its default. The default key includes the full query
string, so each post's `?url=…` caches as a separate entry — which is exactly
what we want. Do not add a cache-busting query parameter in the widget.

### Verifying

```
curl -sI "https://ap.vrypan.net/api/interactions?url=https://blog.vrypan.net/some-post/" \
  | grep -i -E 'cf-cache-status|cache-control|etag'
```

- First request: `cf-cache-status: MISS`
- Repeat within 30 s: `cf-cache-status: HIT`
- After 30 s: `cf-cache-status: HIT` again, but served stale while Cloudflare
  revalidates in the background (`REVALIDATED`/`EXPIRED` may also appear).

If you only ever see `DYNAMIC`, the Cache Rule isn't matching — re-check the
path/hostname expression.

---

## 2. Tiered Cache (strongly recommended)

Dashboard → **Caching → Tiered Cache → enable "Tiered Cache"** (Smart Tiered
Cache Topology). Free on all plans.

Without it, "one origin fetch per 30 s" is **per Cloudflare data center**. A
globally popular post can still produce dozens of simultaneous origin misses,
one per PoP. Tiered Cache routes misses through a single upper-tier PoP, so a
global burst collapses to one origin request. This is the biggest single win
for burst protection.

---

## 3. Things that silently break edge caching

- **Cookies.** If the origin sets a `Set-Cookie` on this response, or the
  request carries cookies, Cloudflare bypasses the cache. listnr's interactions
  endpoint sets no cookies — keep it that way, and don't put it behind anything
  that injects session cookies.
- **A cache-busting query param** in the widget (`&t=<random>`) — every value
  is a distinct cache key, so nothing is ever a hit. Don't add one.
- **`no-cache` / `max-age=0` without `s-maxage`.** Cloudflare treats those as
  "revalidate every time," i.e. no edge caching. listnr avoids this by sending
  `s-maxage`; if you override the header, keep an `s-maxage`.

---

## 4. Optional upgrade: purge on update (instant freshness)

The setup above trades up to ~30 s of staleness for burst protection. If you
want both full edge caching **and** sub-second freshness, cache at the edge for
much longer and have listnr purge a post's cached entry the moment its
reactions change.

Sketch:

1. Raise the edge TTL — either send `s-maxage=86400` from listnr, or set a
   fixed Edge TTL of 1 day in the Cache Rule.
2. Create a Cloudflare API token scoped to **Zone → Cache Purge → Purge**.
3. When an interaction is inserted / edited / hidden / deleted for a post,
   listnr calls the purge-by-URL API for that post's exact widget URL:

   ```
   POST https://api.cloudflare.com/client/v4/zones/<zone_id>/purge_cache
   Authorization: Bearer <token>
   Content-Type: application/json

   {"files": ["https://ap.vrypan.net/api/interactions?url=<exact-encoded-post-url>"]}
   ```

   The URL must match the cached request byte-for-byte, including the
   percent-encoding of the `url` query value.

Caveats: purge-by-URL is available on all plans (Cache-Tag purge, which would
be cleaner, is Enterprise-only); purges are eventually consistent (a few
seconds); and the calls should be best-effort and non-blocking (debounce
bursts of interactions for the same post, and never fail a request because a
purge failed — the short `s-maxage`/TTL is your safety net either way).

This is deliberately **not** implemented yet: it needs a Cloudflare API token
in `listnr.toml` and purge hooks in the inbox/admin mutation paths. Start with
sections 1–2; add this only if the 30 s window turns out to matter.
