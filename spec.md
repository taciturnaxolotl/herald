# rss2email-ssh

A minimal, SSH-powered RSS to email service. Upload a feed config via SCP, get email digests on a schedule.

## Goals

- **Simple**: Single binary, SQLite storage, no external dependencies
- **SSH-native**: Auth via SSH keys, configure via SCP or interactive TUI
- **Pico-compatible**: Same config format as pico.sh/feeds
- **Charm-powered**: Built with the Charm ecosystem

## Architecture

```
                    ┌─────────────────────────────────────────┐
                    │            rss2email-ssh                │
                    ├─────────────────────────────────────────┤
                    │                                         │
   SSH key auth     │  ┌─────────────┐    ┌──────────────┐   │
   ─────────────────┼─▶│   wish      │───▶│  bubbletea   │   │
                    │  │  (SSH srv)  │    │  (TUI)       │   │
                    │  └─────────────┘    └──────────────┘   │
                    │         │                              │
   SCP upload       │         ▼                              │
   ─────────────────┼─▶┌─────────────┐    ┌──────────────┐   │
                    │  │  wish/scp   │───▶│   SQLite     │   │
                    │  │  (files)    │    │   (store)    │   │
                    │  └─────────────┘    └──────┬───────┘   │
                    │                            │           │
                    │                            ▼           │
                    │                     ┌──────────────┐   │
                    │                     │  Scheduler   │   │
                    │                     │  (cron loop) │   │
                    │                     └──────┬───────┘   │
                    │                            │           │
                    │                            ▼           │
                    │                     ┌──────────────┐   │
                    │                     │  SMTP out    │──────▶ Email
                    │                     └──────────────┘   │
                    └─────────────────────────────────────────┘
```

## Charm Libraries

| Library | Purpose |
|---------|---------|
| [wish](https://github.com/charmbracelet/wish) | SSH server, middleware, SCP handling |
| [lipgloss](https://github.com/charmbracelet/lipgloss) | Styling CLI output |
| [log](https://github.com/charmbracelet/log) | Structured logging |

## Config Format

Pico-compatible plaintext format. Users upload as `feeds.txt` (or any `.txt` file):

```text
=: email you@example.com
=: cron 0 8 * * *
=: digest true
=: inline false
=> https://blog.example.com/rss
=> https://news.ycombinator.com/rss
=> https://lobste.rs/rss "Lobsters"
```

### Directives

| Directive | Required | Description |
|-----------|----------|-------------|
| `=: email <addr>` | Yes | Recipient email address |
| `=: cron <expr>` | Yes | Standard cron expression (5 fields) |
| `=: digest <bool>` | No | Combine all items into one email (default: true) |
| `=: inline <bool>` | No | Include article content in email (default: false) |
| `=> <url> ["name"]` | Yes (1+) | RSS/Atom feed URL, optional display name |

Note: Items are filtered to only include those published within the last 3 months.

## Data Model

### SQLite Schema

```sql
-- Users identified by SSH public key fingerprint
CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    pubkey_fp TEXT UNIQUE NOT NULL,  -- SHA256 fingerprint
    pubkey TEXT NOT NULL,            -- Full public key
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Feed configurations (one per uploaded file)
CREATE TABLE configs (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id),
    filename TEXT NOT NULL,
    email TEXT NOT NULL,
    cron_expr TEXT NOT NULL,
    digest BOOLEAN DEFAULT TRUE,
    inline_content BOOLEAN DEFAULT FALSE,
    raw_text TEXT NOT NULL,          -- Original file contents
    last_run DATETIME,
    next_run DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, filename)
);

-- Individual feeds within a config
CREATE TABLE feeds (
    id INTEGER PRIMARY KEY,
    config_id INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    name TEXT,                       -- Optional display name
    last_fetched DATETIME,
    etag TEXT,                       -- For conditional requests
    last_modified TEXT
);

-- Seen items for deduplication
CREATE TABLE seen_items (
    id INTEGER PRIMARY KEY,
    feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    guid TEXT NOT NULL,              -- Item GUID or link hash
    title TEXT,
    link TEXT,
    seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(feed_id, guid)
);
```

## SSH Interface

### Authentication

- Public key auth only (no passwords)
- First connection auto-registers user by key fingerprint
- Config file for allowed keys (optional, default: allow all)

```yaml
# config.yaml
allow_all_keys: true  # or false to require explicit registration
allowed_keys:
  - "ssh-ed25519 AAAA... user@host"
```

### SCP Upload

```bash
# Upload a feed config
scp feeds.txt user@rss.example.com:

# Upload with custom name
scp feeds.txt user@rss.example.com:work-feeds.txt

# Download existing config
scp user@rss.example.com:feeds.txt .
```

### CLI Commands

Via SSH command execution:

```bash
# List configs
ssh rss.example.com ls

# Show config contents
ssh rss.example.com cat feeds.txt

# Delete a config
ssh rss.example.com rm feeds.txt

# Run immediately (don't wait for cron)
ssh rss.example.com run feeds.txt

# Show recent activity
ssh rss.example.com logs
```

## Web Interface

Minimal brutalist web UI (à la pierre.computer). Serves two purposes:

1. **Public RSS feed** - Aggregated feed of all items for a user
2. **Config view** - Shows the raw config file

### Routes

```
GET /                       # Landing page
GET /{fingerprint}          # User's public page
GET /{fingerprint}/feed.xml # Aggregated RSS feed
GET /{fingerprint}/feed.json # JSON Feed format
GET /{fingerprint}/{config} # Raw config file
```

### User Page

```
/{fingerprint}
```

```
RSS2EMAIL █

~~~

USER: a]D+3xKL...
STATUS: ONLINE
NEXT RUN: 2025-01-09 08:00 UTC

~~~

CONFIGS:
 - [feeds.txt](/abc123/feeds.txt) (3 feeds)
 - [work.txt](/abc123/work.txt) (5 feeds)

FEEDS:
 - [RSS](/abc123/feed.xml)
 - [JSON](/abc123/feed.json)
```

### Aggregated Feed

`/{fingerprint}/feed.xml` returns a standard RSS 2.0 feed containing all items from all the user's subscribed feeds - essentially a "river of news" feed they can subscribe to elsewhere.

### Styling

```css
* {
    font-family: monospace;
    background: #000;
    color: #fff;
}

a {
    color: #fff;
    text-decoration: underline;
}

pre {
    white-space: pre-wrap;
}
```

Single HTML template, no JS, ~20 lines of CSS.

## Scheduler

Background goroutine that:

1. Every 60 seconds, queries `configs` where `next_run <= now()`
2. For each due config:
   - Fetch all feeds (parallel, with timeout)
   - Filter to unseen items (check `seen_items` table)
   - If new items exist, render and send email
   - Update `last_run`, calculate and set `next_run`
   - Insert new items into `seen_items`
3. Handle errors gracefully (log, increment retry counter)

### Cron Parsing

Use [adhocore/gronx](https://github.com/adhocore/gronx) for cron expression parsing (same as pico).

## Email Rendering

### Digest Mode (default)

One email per config run containing all new items:

```
Subject: RSS Digest: 5 new items
From: rss@example.com
To: you@example.com
Content-Type: multipart/alternative

──────────────────────────────────────
Lobsters (2 new)
──────────────────────────────────────

▸ Show HN: I built a thing
  https://example.com/article1

▸ Why Rust is great
  https://example.com/article2

──────────────────────────────────────
Example Blog (3 new)
──────────────────────────────────────

▸ My latest post
  https://blog.example.com/post1

...
```

### Individual Mode

One email per item (when `digest: false`).

### Templates

Use Go `html/template` and `text/template` for HTML and plaintext versions.

## Project Structure

```
rss2email-ssh/
├── main.go              # Entry point, CLI flags
├── ssh/
│   ├── server.go        # wish server setup, middleware
│   ├── scp.go           # SCP upload/download handlers
│   └── commands.go      # ls, rm, cat, run, logs
├── web/
│   ├── server.go        # HTTP server
│   ├── handlers.go      # Route handlers
│   └── templates/
│       ├── index.html
│       ├── user.html
│       └── style.css
├── config/
│   ├── parse.go         # Parse pico-format config files
│   └── validate.go      # Validation logic
├── store/
│   ├── db.go            # SQLite connection, migrations
│   ├── users.go         # User CRUD
│   ├── configs.go       # Config CRUD
│   └── items.go         # Seen items tracking
├── scheduler/
│   ├── scheduler.go     # Main loop
│   └── fetch.go         # RSS fetching with gofeed
├── email/
│   ├── render.go        # Template rendering
│   ├── send.go          # SMTP sending
│   └── templates/
│       ├── digest.html
│       └── digest.txt
├── go.mod
├── go.sum
└── config.example.yaml
```

## Configuration

Server configuration via YAML or environment variables:

```yaml
# config.yaml
host: 0.0.0.0
port: 2222

# SSH host keys (generated on first run if missing)
host_key_path: ./host_key

# Database
db_path: ./rss2email.db

# SMTP
smtp:
  host: smtp.example.com
  port: 587
  user: sender@example.com
  pass: ${SMTP_PASS}  # Env var substitution
  from: rss@example.com

# Auth
allow_all_keys: true
```

## Dependencies

```go
require (
    github.com/charmbracelet/wish v1.4.0
    github.com/charmbracelet/lipgloss v1.0.0
    github.com/charmbracelet/log v0.4.0
    github.com/mmcdole/gofeed v1.3.0
    github.com/adhocore/gronx v1.19.0
    github.com/mattn/go-sqlite3 v1.14.24
    gopkg.in/yaml.v3 v3.0.1
)
```

## Implementation Phases

### Phase 1: Core (MVP)

- [ ] SSH server with key auth (wish)
- [ ] SCP upload/download
- [ ] Config parsing (pico format)
- [ ] SQLite storage
- [ ] Basic scheduler
- [ ] Plaintext email sending

### Phase 2: Polish

- [ ] Web UI (brutalist style)
- [ ] Aggregated RSS/JSON feeds
- [ ] HTML emails
- [ ] Conditional fetching (ETag/Last-Modified)
- [ ] `logs` command

### Phase 3: Nice-to-have

- [ ] OPML import/export
- [ ] Feed discovery (find RSS from URL)
- [ ] Webhook notifications
- [ ] Metrics endpoint

## Example Session

```bash
# First time setup - just SSH in
$ ssh rss.example.com
Welcome! Your account has been created.
Upload a config with: scp feeds.txt rss.example.com:

# Upload a config
$ cat feeds.txt
=: email me@example.com
=: cron 0 8 * * *
=> https://lobste.rs/rss

$ scp feeds.txt rss.example.com:
feeds.txt                     100%   89    12.3KB/s   00:00
Config saved! Next run: tomorrow at 8:00 AM

# Check status
$ ssh rss.example.com ls
feeds.txt    1 feed    next: 8:00 AM

# Run immediately
$ ssh rss.example.com run feeds.txt
Fetched 25 items, 25 new
Email sent to me@example.com
```
