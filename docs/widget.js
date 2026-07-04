(function () {
  var script = document.currentScript;
  var endpoint = script && script.dataset.endpoint;
  if (!endpoint) return;

  var postURL = new URL(script.dataset.url || window.location.href);
  postURL.hash = "";
  postURL.search = "";

  fetch(endpoint.replace(/\/$/, "") + "/api/interactions?url=" + encodeURIComponent(postURL.toString()))
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (data) {
      if (!data) return;
      var root = document.createElement("section");
      root.className = "listnr-comments";

      var title = document.createElement("h2");
      title.textContent = "Fediverse";
      root.appendChild(title);

      var counts = document.createElement("p");
      counts.className = "listnr-counts";
      counts.textContent = data.likes + " likes · " + data.boosts + " boosts";
      root.appendChild(counts);

      (data.replies || []).forEach(function (reply) {
        var article = document.createElement("article");
        article.className = "listnr-reply";

        var header = document.createElement("p");
        var author = document.createElement("a");
        author.href = reply.author.url;
        author.rel = "nofollow ugc";
        author.textContent = reply.author.name || reply.author.handle || reply.author.url;
        header.appendChild(author);
        if (reply.published) {
          var time = document.createElement("time");
          time.dateTime = reply.published;
          time.textContent = " " + new Date(reply.published).toLocaleString();
          header.appendChild(time);
        }
        article.appendChild(header);

        var body = document.createElement("div");
        body.className = "listnr-reply-body";
        body.innerHTML = reply.content_html || "";
        article.appendChild(body);
        root.appendChild(article);
      });

      document.body.appendChild(root);
    })
    .catch(function () {});
})();
