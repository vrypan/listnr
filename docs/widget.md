# listnr widget

Add the script near the end of each blog post page:

```html
<script src="/path/to/widget.js" data-endpoint="https://ap.vrypan.net"></script>
```

The script strips query strings and fragments from the current page URL,
fetches `https://ap.vrypan.net/api/interactions?url=<post-url>`, and appends a
small Fediverse comments section to the page. Server-side reply HTML is already
sanitized; all other values are inserted as text.

