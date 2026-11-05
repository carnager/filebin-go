# filebin-go

A simple, self-hosted file and paste server with syntax highlighting, media support, collections, and a dark-mode web UI.

> **Warning:** This project was largely written by an LLM (Claude). While it works, the code has not been thoroughly audited. Use at your own risk and review before deploying in sensitive environments.

## Features

- **File uploads** with deduplication (MD5), progress bar, drag & drop anywhere and clipboard paste (Ctrl+V)
- **Paste support** with syntax highlighting (chroma; monokai in dark mode, github in light mode), language override, line wrap toggle
- **Markdown rendering** for `.md` pastes (GitHub flavoured, raw HTML stripped) with rendered/source toggle
- **Media preview** for images, video, audio, and asciinema recordings
- **Thumbnails** for JPEG/PNG/GIF/WebP (cached on disk, EXIF orientation honoured)
- **Collections** — named groups of files, editable membership, download as .tar
- **Expiry** per upload (1 hour … 1 month, or never) and **burn after reading**
- **Password protection** for files and collections (bcrypt)
- **Share bar** — copy link / raw link, download, QR code, info; rename and delete when logged in
- **Link previews** (OpenGraph / Twitter cards), suppressed for protected and burn-after-reading items
- **History** with search, type filters, sorting, thumbnails and a mobile card layout
- **Gallery** with lightbox viewer (keyboard, swipe, download / open original)
- **Theme**: follows the system, or force light / dark
- **Auto-cleanup** of expired items, plus optional global retention
- **API** with Bearer token auth for programmatic access
- **CLI client** (`fb`) with upload, download, delete, history, expiry, burn and collection support
- **Arch Linux packaging** with systemd service, sysusers, tmpfiles, and Thunar and [Tandem](https://github.com/carnager/tandem-fm) integration

## Quick Start

```sh
go build -o filebin .
echo "your-secret-key" >> data/apikeys
./filebin
```

Server starts on `:7995` by default.

### Options

```
-addr :7995          listen address
-data ./data         data directory
-max-upload-mb 256   max upload size per request in MB
-max-age 90          auto-delete everything after N days (0 = disabled)
```

Per-item expiry (`expires`) is always enforced: expired items return 404 immediately and are
removed by a cleanup pass every 10 minutes, together with collections whose files are all gone.
The data directory holds `files/` (blobs), `meta/` and `collections/` (JSON), `thumbs/`
(thumbnail cache), `apikeys` and `secret` (HMAC key for unlock cookies, created on first start).

## CLI Client

The `fb` client is a standalone bash script that talks to the API.

```sh
fb file.txt                  # upload
fb -p SECRET file.txt        # upload with password
fb -e 1d file.txt            # expire after a day (1h, 1d, 1w, 1m, never, or seconds)
fb -b secret.txt             # burn after reading
fb -C a.png b.png c.png      # upload and create collection
fb -C -n Trip *.jpg          # ... named "Trip"
echo "hello" | fb            # pipe to upload
fb -d ID                     # delete
fb -dF ID                    # delete collection and its files
fb -g ID                     # download to stdout
fb -g -p SECRET ID           # download password-protected file
fb -H                        # history (table)
fb -Hr                       # history (tsv, machine-readable)
fb -c -n Name ID ID          # create collection from existing files
```

### Configuration

```sh
# ~/.config/fb/config
FB_SERVER=https://your-server.example.com

# ~/.config/fb/apikey
your-secret-key
```

## API

All `/api/*` endpoints except `/api/config` require an `Authorization: Bearer <key>` header.
Errors are JSON: `{"error": "message"}` with a matching status code. Password hashes are never returned.

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/upload` | Multipart: `file` (repeatable, max 20), optional `password`, `expires` (`1h`, `1d`, `1w`, `1m`, `never` or seconds), `burn` (`true`/`1`), `collection` (`true` → also create a collection), `collection_name`. Returns 201 `{"files":[File], "collection": Collection\|null}` |
| GET | `/api/history` | `{"files":[File], "collections":[Collection], "total_size", "total_files", "total_collections"}`. Optional `?q=` (id/name), `?type=image\|video\|audio\|text\|other\|collection`, `?limit=&offset=` (applied to files and collections merged, newest first; totals are counted before paging) |
| GET | `/api/{id}` | `{"type":"file","file":File}` or `{"type":"collection","collection":Collection,"files":[File]}` |
| PATCH | `/api/{id}` | JSON, all fields optional: `name`, `password` (`""` removes), `expires`; collections also `add` / `remove` (file ID lists). Returns the same shape as GET |
| DELETE | `/api/{id}` | Delete file or collection (`?files=true` also deletes the collection's files) |
| POST | `/api/collection` | `{"ids":[...], "name":"", "password":"", "expires":"never"}` → 201 Collection |
| GET | `/api/config` | Public: `max_upload_size`, `max_files_per_request`, `expiry_options`, `default_expiry`, `version` |

File: `{"id","name","mimetype","hash","size","date","expires","burn","protected","url","thumb"}`
(`expires` is a unix timestamp, 0 = never).
Collection: `{"id","name","files","date","expires","protected","url","size"}`.

### Other routes

| Route | Description |
|-------|-------------|
| `/{id}` | Web view (paste, media, collection); raw content for non-browser clients |
| `/{id}/raw` | Raw content; `?download=1` forces a download |
| `/{id}/{lang}` | Paste with a forced highlighting language (e.g. `/abc/python`) |
| `/{id}/thumb` | JPEG thumbnail (max 400px), 404 for non-images |
| `/{id}/qr.png` | QR code of the item URL |
| `/{id}/info` | Details page |
| `/{id}/tar` | Collection as tarball |

Password-protected items accept an `X-Password` header or `?password=` query parameter.
Requests carrying a valid Bearer key (or the web login cookie) skip password checks and never trigger
burn-after-reading, so owners can preview their own uploads. Link-preview bots never consume a burn item.

## Arch Linux

A split PKGBUILD is included in `pkg/`:

```sh
cd pkg && makepkg -si
```

This installs:
- `filebin` — server binary, systemd service, sysusers/tmpfiles
- `fb` — CLI client, `fb-upload` helper, Thunar sendto integration and a Tandem plugin

The Tandem plugin adds **Upload to filebin** and **Upload to filebin for** (1 hour … 1 month,
or burn after reading) to the context menu. Switch it on in Settings › Plugins. Several files
become one collection; the link is copied and shown in the status line.

```sh
# Add your API key
echo "your-secret-key" | sudo tee -a /var/lib/filebin/apikeys

# Start the service
sudo systemctl enable --now filebin
```

## Android Client

An Android client is available at [filebin-android](https://github.com/carnager/filebin-android) — built with Kotlin and Jetpack Compose (Material 3), with upload, share intent, history, gallery, collections, and password protection.

## Credits

Inspired by [fb](https://github.com/Bluewind/filebin) by Florian Pritz (Bluewind).

## License

[GPLv3](LICENSE)
