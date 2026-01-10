# Herald 🎏

![email from harold](https://l4.dunkirk.sh/i/Ck271POS5n0k.webp)

This was inspired by the sunsetting of [pico.sh/feeds](https://blog.pico.sh/ann-033-moving-rss-to-email-pico-plus) being available outside of `pico+`. It is a totally understandable move from them as their email costs were skyrocketing and they needed to pay for it somehow. This was created to allow me to still get my rss feeds delivered to me each day by email which I have grown quite accustomed to. The config is completely compatible with the `pico.sh` format as of `2026-01-09` and should stay fairly stable. It is also configured over ssh with the slight addition that you can view your feeds on a website as well as I found myself wanting to hot load my feeds into my website :)

The canonical repo for this is hosted on tangled over at [`dunkirk.sh/herald`](https://tangled.org/@dunkirk.sh/herald)

## Quick Start

```bash
# Build
go build -ldflags "-X main.commitHash=$(git log -1 --format=%H)" -o herald .

# Run the server
./herald serve

# Or with a config file
./herald serve -c config.yaml
```

> **Note:** The commit hash is automatically detected at runtime if not embedded at build time.

## Usage

### Upload a config

Create a `feeds.txt` file:

```text
=: email you@example.com
=: cron 0 8 * * *
=: digest true
=> https://dunkirk.sh/atom.xml
=> https://news.ycombinator.com/rss
=> https://lobste.rs/rss "Lobsters"
```

Upload via SCP:

```bash
scp feeds.txt user@herald.dunkirk.sh:
```

### SSH Configuration

Add this to your `~/.ssh/config` for easier access:

```ssh-config
Host herald
    HostName herald.dunkirk.sh
    Port 2223
    User herald
```

Then use: `scp feeds.txt herald:` and `ssh herald ls`

### SSH Commands

```bash
# Get your fingerprint (for web dashboard)
ssh herald.dunkirk.sh

# List your configs
ssh herald.dunkirk.sh ls

# Show config contents
ssh herald.dunkirk.sh cat feeds.txt

# Delete a config
ssh herald.dunkirk.sh rm feeds.txt

# Activate/deactivate configs
ssh herald.dunkirk.sh activate feeds.txt
ssh herald.dunkirk.sh deactivate feeds.txt

# Run immediately (don't wait for cron)
ssh herald.dunkirk.sh run feeds.txt

# Show recent activity
ssh herald.dunkirk.sh logs
```

### Web Interface

Visit `http://localhost:8080` for the landing page.

After uploading a config, run `ssh herald.dunkirk.sh` to get your fingerprint, then visit:

- `http://localhost:8080/{fingerprint}` - Your dashboard with config status
- `http://localhost:8080/{fingerprint}/feeds.xml` - RSS feed for feeds.txt
- `http://localhost:8080/{fingerprint}/feeds.json` - JSON feed for feeds.txt

## Config Format

### Directives

| Directive           | Required | Description                                       |
| ------------------- | -------- | ------------------------------------------------- |
| `=: email <addr>`   | Yes      | Recipient email address                           |
| `=: cron <expr>`    | Yes      | Standard cron expression (5 fields)               |
| `=: digest <bool>`  | No       | Combine all items into one email (default: true)  |
| `=: inline <bool>`  | No       | Include article content in email (default: false) |
| `=> <url> ["name"]` | Yes (1+) | RSS/Atom feed URL, optional display name          |

## Configuration

Create a `config.yaml`:

```yaml
host: 0.0.0.0
ssh_port: 2222
http_port: 8080

host_key_path: ./host_key
db_path: ./herald.db

smtp:
  host: smtp.example.com
  port: 587
  user: sender@example.com
  pass: ${SMTP_PASS}
  from: herald@example.com

allow_all_keys: true
```

Environment variables can also be used:

- `HERALD_HOST`
- `HERALD_SSH_PORT`
- `HERALD_HTTP_PORT`
- `HERALD_DB_PATH`
- `HERALD_SMTP_HOST`
- `HERALD_SMTP_PORT`
- `HERALD_SMTP_USER`
- `HERALD_SMTP_PASS`
- `HERALD_SMTP_FROM`

<p align="center">
    <img src="https://raw.githubusercontent.com/taciturnaxolotl/carriage/main/.github/images/line-break.svg" />
</p>

<p align="center">
    <i><code>&copy 2025-present <a href="https://dunkirk.sh">Kieran Klukas</a></code></i>
</p>

<p align="center">
    <a href="https://tangled.org/dunkirk.sh/herald/blob/main/LICENSE.md"><img src="https://img.shields.io/static/v1.svg?style=for-the-badge&label=License&message=O'Saasy&logoColor=d9e0ee&colorA=363a4f&colorB=b7bdf8"/></a>
    <a href="https://codecov.io/gh/taciturnaxolotl/harold"><img src="https://img.shields.io/codecov/c/github/taciturnaxolotl/harold?style=for-the-badge&logoColor=d9e0ee&colorA=363a4f&colorB=a6da95"/></a>
</p>
