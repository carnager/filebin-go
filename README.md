# filebin-go

A simple, self-hosted file and paste server with syntax highlighting, media support, collections, and a dark-mode web UI.

## Features

- **File uploads** with deduplication (MD5)
- **Paste support** with syntax highlighting (chroma, monokai theme)
- **Media preview** for images, video, audio, and asciinema recordings
- **Collections** — group files together, download as .tar
- **Gallery** with lightbox viewer and keyboard navigation
- **Password protection** for files and collections (bcrypt)
- **Dark mode** by default, with light mode toggle (respects system preference)
- **Auto-cleanup** of old files with configurable retention
- **API** with Bearer token auth for programmatic access
- **CLI client** (`fb`) with upload, download, delete, history, and collection support
- **Arch Linux packaging** with systemd service, sysusers, tmpfiles, and Thunar integration

## Quick Start

```sh
go build -o filebin .
echo "your-secret-key" >> data/apikeys
./filebin
```

Server starts on `:7995` by default.

### Options

```
-addr :7995         listen address
-data ./data        data directory
-max-upload 50MB    max upload size
-max-age 90         auto-delete after N days (0 = disabled)
```

## CLI Client

The `fb` client is a standalone bash script that talks to the API.

```sh
fb file.txt                  # upload
fb -p SECRET file.txt        # upload with password
fb -C a.png b.png c.png      # upload and create collection
echo "hello" | fb            # pipe to upload
fb -d ID                     # delete
fb -dF ID                    # delete collection and its files
fb -g ID                     # download to stdout
fb -g -p SECRET ID           # download password-protected file
fb -H                        # history (table)
fb -Hr                       # history (tsv, machine-readable)
fb -c ID ID                  # create collection from existing files
```

### Configuration

```sh
# ~/.config/fb/config
FB_SERVER=https://your-server.example.com

# ~/.config/fb/apikey
your-secret-key
```

## API

All API endpoints require `Authorization: Bearer <key>` header.

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/upload` | Upload files (multipart, field: `file`, optional: `password`) |
| GET | `/api/history` | List all files and collections |
| DELETE | `/api/{id}` | Delete file or collection (`?files=true` to also delete collection files) |
| POST | `/api/collection` | Create collection (`{"ids": [...], "password": "..."}`) |
| GET | `/api/config` | Public server config |

Password-protected files can be accessed with `X-Password` header or `?password=` query parameter.

## Arch Linux

A split PKGBUILD is included in `pkg/`:

```sh
cd pkg && makepkg -si
```

This installs:
- `filebin` — server binary, systemd service, sysusers/tmpfiles
- `fb` — CLI client, `fb-upload` helper, Thunar sendto integration

```sh
# Add your API key
echo "your-secret-key" | sudo tee -a /var/lib/filebin/apikeys

# Start the service
sudo systemctl enable --now filebin
```

## Credits

Inspired by [fb](https://github.com/Bluewind/filebin) by Florian Pritz (Bluewind).

## License

[GPLv3](LICENSE)
