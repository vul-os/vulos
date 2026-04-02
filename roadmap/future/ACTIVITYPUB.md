# ActivityPub — Unified Social Client

A single web app that combines microblogging, photos, video, and forums — all from the Fediverse via the ActivityPub protocol. One client, one account, replaces Twitter, Instagram, YouTube, and Reddit.

---

## Why

- ActivityPub is a W3C standard — not controlled by any company
- One protocol connects: Mastodon (Twitter), Pixelfed (Instagram), PeerTube (YouTube), Lemmy (Reddit), BookWyrm (Goodreads), Funkwhale (Spotify/SoundCloud)
- Millions of users across thousands of servers
- No ads, no algorithm manipulation, no platform lock-in
- User owns their identity and can move between servers

---

## Architecture

```
┌─────────────────────────────────────────────────┐
│  Vula Social (web UI in WebKit)                  │
│                                                   │
│  ┌──────────┐ ┌──────────┐ ┌────────┐ ┌───────┐ │
│  │  Feed    │ │  Photos  │ │ Video  │ │ Forum │ │
│  │ (Twitter)│ │ (Insta)  │ │ (YT)   │ │(Reddit)│ │
│  └────┬─────┘ └────┬─────┘ └───┬────┘ └───┬───┘ │
│       └─────────┬───┴───────┬───┘          │     │
│                 │ ActivityPub API           │     │
└─────────────────┼──────────────────────────┼─────┘
                  │                          │
┌─────────────────▼──────────────────────────▼─────┐
│  ActivityPub Server (local or remote)             │
│                                                    │
│  Option A: GoToSocial (lightweight, single user)   │
│  Option B: Connect to existing Mastodon account    │
│  Option C: No server — read-only public feeds      │
│                                                    │
│  Federation ←→ Fediverse                           │
└────────────────────────────────────────────────────┘
```

---

## UI — Four views, one app

### Feed (replaces Twitter/X)

- Timeline: home (followed accounts), local (your server), federated (all)
- Post: text up to 500 chars (server-configurable), images, polls, content warnings
- Interactions: boost (retweet), favourite (like), reply, bookmark
- Hashtag follow and trending topics
- Thread view for conversations

### Photos (replaces Instagram)

- Grid view of image posts from followed Pixelfed/Mastodon accounts
- Full-screen image viewer with swipe
- Photo upload with filters (CSS filters, client-side)
- Album/carousel support (ActivityPub supports multiple attachments)
- Explore: discover photographers via hashtags and trending

### Video (replaces YouTube)

- Video feed from followed PeerTube channels
- Inline playback (HLS via hls.js)
- Subscribe to PeerTube channels (they're ActivityPub actors)
- Comments (ActivityPub replies)
- Upload to your own PeerTube instance if configured

### Forums (replaces Reddit)

- Browse Lemmy communities
- Upvote/downvote (Lemmy extension to ActivityPub)
- Post and comment
- Subscribe to communities across any Lemmy instance
- Sort: hot, new, top, active

---

## Open-source clients to build on or reference

| Client | What | Tech | License | Notes |
|--------|------|------|---------|-------|
| [Elk](https://github.com/elk-zone/elk) | Mastodon web client | Vue/Nuxt | MIT | Beautiful, modern. Best UI reference |
| [Phanpy](https://github.com/nicoleahmed/phanpy) | Mastodon web client | Preact | MIT | Lightweight, fast, innovative UI |
| [Semaphore](https://github.com/nicoleahmed/semaphore) | Mastodon web client | Svelte | AGPLv3 | Performance-focused, accessible |
| [Pixelfed Web](https://github.com/pixelfed/pixelfed) | Photo sharing | PHP/Vue | AGPLv3 | Instagram-like, reference for photo UI |
| [Lemmy UI](https://github.com/LemmyNet/lemmy-ui) | Forum client | Inferno/TS | AGPLv3 | Reference for forum views |
| [PeerTube](https://github.com/Chocobozzz/PeerTube) | Video platform | Angular/TS | AGPLv3 | Reference for video player/feeds |

### Recommendation

**Elk** as the starting point for the feed view — MIT license, Vue/Nuxt, excellent UX. Extend it with photo grid, video player, and forum views to create the unified super client.

Alternatively, build from scratch in React (matches our stack) using the Mastodon client API directly — it's a well-documented REST API.

---

## Server options (for users who want their own identity)

| Server | Language | License | Notes |
|--------|----------|---------|-------|
| [GoToSocial](https://github.com/superseriousbusiness/gotosocial) | Go | AGPLv3 | **Best fit** — single binary, SQLite, lightweight, designed for single-user/small instances. Ideal for Vula OS |
| [Mastodon](https://github.com/mastodon/mastodon) | Ruby | AGPLv3 | Reference implementation but heavy (Ruby, PostgreSQL, Redis, Sidekiq) |
| [Pleroma](https://git.pleroma.social/pleroma/pleroma) | Elixir | AGPLv3 | Lightweight, single binary possible. Good alternative to GoToSocial |
| [Akkoma](https://akkoma.dev/AkkomaGang/akkoma) | Elixir | AGPLv3 | Pleroma fork, more active development |

### Recommendation

**GoToSocial** — written in Go (matches our backend), single binary, SQLite, designed to be lightweight. Can run as a Vula OS service alongside Conduit (Matrix). Users get their own Fediverse identity: `@user@their-vula-device.local`

---

## Three usage modes

### 1. Read-only (no server needed)
- Browse public feeds, trending posts, hashtags
- Follow public Fediverse accounts
- No posting, no identity
- Zero setup — works immediately

### 2. Existing account
- Log in with existing Mastodon/Pixelfed/Lemmy account
- Full posting and interaction
- Uses remote server's API (OAuth2)
- No local server needed

### 3. Self-hosted identity (GoToSocial)
- Run GoToSocial as a Vula OS service
- Own your identity: `@you@your-domain`
- Federate with the entire Fediverse
- Full control over your data

---

## ActivityPub API basics

The Mastodon client API (REST) is the de facto standard — supported by Mastodon, Pleroma, GoToSocial, Akkoma, Pixelfed, and more.

Key endpoints:
- `GET /api/v1/timelines/home` — home feed
- `GET /api/v1/timelines/public` — federated/local feed
- `POST /api/v1/statuses` — create a post
- `GET /api/v1/accounts/:id/statuses` — user's posts
- `POST /api/v1/statuses/:id/favourite` — like
- `POST /api/v1/statuses/:id/reblog` — boost
- `GET /api/v1/trends/tags` — trending hashtags
- `GET /api/v1/notifications` — notifications
- `GET /api/v2/search` — search users, posts, hashtags
- OAuth2 for authentication

Streaming via WebSocket: `wss://instance/api/v1/streaming`

---

## Integration with Vula OS

- [ ] Ship as a default app: `social.vulos` in the app launcher
- [ ] Push notifications via the Vula OS notification system (WebSocket from streaming API)
- [ ] Share to social from any app (file manager, browser, photos → share → post to Fediverse)
- [ ] AI integration: "summarize my feed", "draft a reply to this thread"
- [ ] Profile links in contacts app (Fediverse handle as a contact field)
- [ ] Offline: cache feeds locally, queue posts when offline, send when back online

---

## TODO

1. [ ] Decide: build on Elk (Vue) or build from scratch in React
2. [ ] Feed view — timeline, posting, interactions
3. [ ] Photos view — grid, full-screen viewer, upload
4. [ ] Video view — PeerTube feed, HLS playback
5. [ ] Forums view — Lemmy communities, upvote/downvote, posting
6. [ ] GoToSocial as optional Vula OS service
7. [ ] OAuth2 login for existing Mastodon/Pixelfed accounts
8. [ ] Push notifications integration
9. [ ] Share-to-social from other Vula OS apps
10. [ ] Offline support — feed caching, queued posts
