package feed

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/vrypan/listnr/internal/buildinfo"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/publish"
	"github.com/vrypan/listnr/internal/store"
)

const maxFeedBody = 1 << 20

type Deliverer interface {
	FanOut(activityJSON []byte) error
}

type Poller struct {
	cfg     *config.Config
	st      *store.Store
	deliver Deliverer
	http    *http.Client
	log     *slog.Logger
	trigger chan chan error
}

func NewPoller(cfg *config.Config, st *store.Store, deliver Deliverer, log *slog.Logger) *Poller {
	return &Poller{
		cfg:     cfg,
		st:      st,
		deliver: deliver,
		http: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		log:     log,
		trigger: make(chan chan error),
	}
}

func (p *Poller) Run(ctx context.Context) {
	interval := p.cfg.Feed.PollInterval.Value()
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case reply := <-p.trigger:
			reply <- p.Poll(ctx)
		case <-timer.C:
			if err := p.Poll(ctx); err != nil {
				p.log.Error("feed poll failed", "err", err)
			}
			timer.Reset(interval)
		}
	}
}

func (p *Poller) Trigger(ctx context.Context) error {
	reply := make(chan error, 1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.trigger <- reply:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

func (p *Poller) Poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Feed.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	if etag, _ := p.st.GetState("feed.etag"); etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lm, _ := p.st.GetState("feed.last_modified"); lm != "" {
		req.Header.Set("If-Modified-Since", lm)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("feed fetch HTTP %d", resp.StatusCode)
	}
	parsed, err := gofeed.NewParser().Parse(io.LimitReader(resp.Body, maxFeedBody))
	if err != nil {
		return err
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		if err := p.st.SetState("feed.etag", etag); err != nil {
			return err
		}
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if err := p.st.SetState("feed.last_modified", lm); err != nil {
			return err
		}
	}
	return p.ingest(parsed)
}

func (p *Poller) ingest(f *gofeed.Feed) error {
	items := append([]*gofeed.Item(nil), f.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		return itemTime(items[i]).After(itemTime(items[j]))
	})
	total, err := p.st.TotalPostCount()
	if err != nil {
		return err
	}
	firstRun := total == 0
	for idx, item := range items {
		guid := itemGUID(item)
		link := item.Link
		if link == "" {
			link = guid
		}
		if guid == "" || link == "" {
			continue
		}
		title := item.Title
		summary := item.Description
		if summary == "" {
			summary = item.Content
		}
		hash := publish.ContentHash(title, summary, link)
		existing, err := p.st.GetPostByGUID(guid)
		if err != nil {
			return err
		}
		if existing == nil {
			if err := p.insertNew(item, guid, link, title, summary, hash, firstRun, idx); err != nil {
				return err
			}
			continue
		}
		if existing.ContentHash != hash && existing.APID.Valid {
			if err := p.updateExisting(existing, title, summary, hash); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Poller) insertNew(item *gofeed.Item, guid, link, title, summary, hash string, firstRun bool, idx int) error {
	published := itemTime(item).UTC().Format(time.RFC3339)
	apID := ""
	announce := ""
	if !firstRun || idx < p.cfg.Feed.Backfill {
		apID = publish.NoteID(p.cfg.Actor.Host, guid)
		announce = store.NowString()
	}
	post := &store.Post{
		GUID: guid, URL: link, Title: title, SummaryHTML: publish.SummaryHTML(summary),
		PublishedAt: published, ContentHash: hash, APID: store.NullString(apID),
		AnnouncedAt: store.NullString(announce),
	}
	id, err := p.st.InsertPost(post)
	if err != nil {
		return err
	}
	post.ID = id
	if firstRun {
		return nil
	}
	create, err := publish.Marshal(publish.Create(p.cfg.Actor, post))
	if err != nil {
		return err
	}
	p.log.Info("new feed item", "url", link, "ap_id", apID)
	return p.deliver.FanOut(create)
}

func (p *Poller) updateExisting(post *store.Post, title, summary, hash string) error {
	updated := time.Now().UTC()
	post.Title = title
	post.SummaryHTML = publish.SummaryHTML(summary)
	post.ContentHash = hash
	post.UpdatedAt = sql.NullString{String: updated.Format(time.RFC3339), Valid: true}
	if err := p.st.UpdatePostContent(post.GUID, post.Title, post.SummaryHTML, post.ContentHash, post.UpdatedAt.String); err != nil {
		return err
	}
	act, err := publish.Marshal(publish.Update(p.cfg.Actor, post, updated))
	if err != nil {
		return err
	}
	p.log.Info("updated feed item", "url", post.URL, "ap_id", post.APID.String)
	return p.deliver.FanOut(act)
}

func itemGUID(item *gofeed.Item) string {
	if item.GUID != "" {
		return item.GUID
	}
	return item.Link
}

func itemTime(item *gofeed.Item) time.Time {
	if item.PublishedParsed != nil {
		return item.PublishedParsed.UTC()
	}
	if item.UpdatedParsed != nil {
		return item.UpdatedParsed.UTC()
	}
	return time.Now().UTC()
}
