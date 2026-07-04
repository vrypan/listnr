# listnr widget

Add the script near the end of each blog post page:

```html
<script src="/path/to/widget.js" data-endpoint="https://ap.vrypan.net"></script>
```

To pin the post URL explicitly, add `data-url`:

```html
<script
  src="/path/to/widget.js"
  data-endpoint="https://ap.vrypan.net"
  data-url="https://blog.vrypan.net/2026/07/example/"></script>
```

The script uses `data-url` when present and falls back to the current page URL.
It strips query strings and fragments, fetches
`https://ap.vrypan.net/api/interactions?url=<post-url>`, and appends a small
Fediverse comments section to the page. Server-side reply HTML is already
sanitized; all other values are inserted as text.
