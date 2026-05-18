# ClawFlow Cloud — Self-host on Aliyun (or any Linux box)

This bundle deploys `clawflow cloud serve` behind Caddy on a single VM. The
target architecture is a single Go binary + SQLite file + Caddy reverse
proxy. No Docker, no external database.

End-to-end:

```
  Browser / CLI                           Worker (dev box)
       │                                       │
       │  HTTPS https://clawflow.daboluo.cc    │
       ▼                                       │
  ┌────────────────────────────────┐           │
  │ Aliyun VM                      │           │
  │  ┌──────────────┐              │           │
  │  │ Caddy :443   │  auto-LE     │           │
  │  └──────┬───────┘              │           │
  │         │ reverse_proxy        │           │
  │  ┌──────▼───────────┐          │           │
  │  │ clawflow cloud   │◄─────────┼───────────┘
  │  │   serve :8790    │   /api/worker/*
  │  │ + SQLite state.db│
  │  └──────────────────┘
  └────────────────────────────────┘
```

## 0. Prerequisites

- Aliyun (or any) VM running Ubuntu 22.04+ or Debian 12.
- A domain that resolves to the VM (`clawflow.daboluo.cc` → public IPv4).
- Port 80 and 443 open in the security group (port 80 is required for
  Let's Encrypt HTTP-01 challenge).
- A GitHub App configured according to the next section.

## 1. Create the GitHub App

Go to **GitHub → Settings → Developer settings → GitHub Apps → New GitHub App**.

| Field | Value |
|---|---|
| **GitHub App name** | `clawflow` (or your own name) |
| **Homepage URL** | `https://clawflow.daboluo.cc` |
| **Callback URL** | `https://clawflow.daboluo.cc/api/v1/github/app/callback` |
| **Request user authorization (OAuth) during installation** | ✅ |
| **Enable Device Flow** | ✅ |
| **Webhook → Active** | ✅ |
| **Webhook URL** | `https://clawflow.daboluo.cc/api/v1/github/app/webhook` |
| **Webhook secret** | (generate one, save it for `CLAWFLOW_GITHUB_APP_WEBHOOK_SECRET`) |
| **Permissions: Repository → Issues** | Read & write |
| **Permissions: Repository → Pull requests** | Read & write |
| **Permissions: Repository → Contents** | Read & write |
| **Permissions: Repository → Metadata** | Read |
| **Subscribe to events** | Issues, Pull request, Pull request review, Push, Label |

After creation:

1. **Note the App ID** (top of the page, e.g. `3451934`).
2. **Note the Client ID** (e.g. `Iv23liuuIvgr8pI22R9C`).
3. **Generate a client secret** under "Client secrets" — copy immediately,
   it's only shown once.
4. **Generate a private key** (`.pem` download) — only needed if you want
   the cloud server to mint installation tokens. Optional for PR 1.
5. **Install the App on at least one repo** so webhooks have somewhere to fire.

## 2. Install on the VM

SSH to the VM and run:

```bash
sudo bash deploy/install.sh
# — OR, if you've already built a binary locally —
scp clawflow_linux_amd64 root@vm:/tmp/clawflow
ssh root@vm sudo bash deploy/install.sh /tmp/clawflow
```

The script:

1. Creates the `clawflow` system user and `/var/lib/clawflow/` data dir.
2. Installs the binary to `/usr/local/bin/clawflow`.
3. Drops `/etc/clawflow/cloud.env` (template), the systemd unit, and the
   Caddyfile in place.
4. Installs Caddy via apt if missing.
5. Enables the service but does **not** start it — you must populate
   `/etc/clawflow/cloud.env` first.

## 3. Populate secrets

Edit `/etc/clawflow/cloud.env` with the values from step 1:

```bash
sudo $EDITOR /etc/clawflow/cloud.env
```

Generate the session key:

```bash
openssl rand -hex 32
```

Then chmod / chown so only the `clawflow` user can read:

```bash
sudo chown root:clawflow /etc/clawflow/cloud.env
sudo chmod 640 /etc/clawflow/cloud.env
```

## 4. Start the service

```bash
sudo systemctl start clawflow-cloud
sudo systemctl status clawflow-cloud
journalctl -u clawflow-cloud -f       # follow logs
```

Caddy reads `/etc/caddy/Caddyfile` automatically. On first request to
`https://clawflow.daboluo.cc`, Caddy obtains a Let's Encrypt certificate.

Smoke-test from the VM itself:

```bash
curl -i http://127.0.0.1:8790/api/v1/auth/me
# → 401 {"error":"not authenticated"}
```

From your laptop:

```bash
curl -i https://clawflow.daboluo.cc/api/v1/auth/me
# → 401 (TLS valid)
```

## 5. Log in from a dev box

```bash
clawflow cloud login --url https://clawflow.daboluo.cc
# → prints a user code and verification URL
# → opens browser; approve the App
# → CLI saves a personal API token to ~/.clawflow/config/credentials.yaml
```

Then register this machine as a worker:

```bash
clawflow worker register --name "$(hostname)"
clawflow worker start
```

## 6. Updating

```bash
sudo bash deploy/install.sh                # re-runs, replaces binary
sudo systemctl restart clawflow-cloud
```

State in `/var/lib/clawflow/state.db` survives binary upgrades; migrations
(`internal/cloud/migrations/`) run on startup.

## 7. Operational notes

- **Backups**: the entire cloud state is one SQLite file at
  `/var/lib/clawflow/state.db`. `sqlite3 state.db ".backup '/path/to/backup.db'"`
  while the service is running is safe (WAL mode).
- **Rotating session key**: changing `CLAWFLOW_SESSION_KEY` invalidates all
  existing browser sessions but does NOT affect API tokens (those are
  hashed with SHA-256, no HMAC key involved).
- **Rotating webhook secret**: change both
  `CLAWFLOW_GITHUB_APP_WEBHOOK_SECRET` and the value in GitHub's App
  settings together, then `systemctl restart clawflow-cloud`.
- **Where the Web UI lives**: there is no Web UI on the cloud server in
  this release. Browser users get a placeholder page after login; full UI
  lands in PR 2.

## 8. Troubleshooting

| Symptom | Check |
|---|---|
| `502 Bad Gateway` from Caddy | `systemctl status clawflow-cloud` — service may have crashed |
| `signature mismatch` on webhook | `CLAWFLOW_GITHUB_APP_WEBHOOK_SECRET` must match what's in the GitHub App settings exactly |
| `redirect_uri_mismatch` on OAuth callback | The App's Callback URL must be `https://<your-domain>/api/v1/github/app/callback` — no trailing slash |
| Device flow polls forever | The user never approved the App in the browser, or the code expired (15 min) |
| `--session-key is required` at startup | Populate `CLAWFLOW_SESSION_KEY` in `/etc/clawflow/cloud.env` |
