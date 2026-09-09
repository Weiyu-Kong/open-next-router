# Updating a Production ONR Deployment

This guide applies to the filesystem and systemd layout documented in
[`START.md`](START.md). It assumes commands run as `root` on the server.

## Production paths

| Purpose | Production path | Update rule |
| --- | --- | --- |
| Source checkout | `/home/open-next-router` | Update with Git or copy a reviewed release into this directory. |
| Gateway binary | `/usr/local/bin/onr` | Rebuild and atomically replace after Go/runtime changes. |
| Admin binary and embedded web UI | `/usr/local/bin/onr-admin` | Rebuild and atomically replace after admin/UI changes. |
| Main production config | `/etc/onr/onr.yaml` | Preserve by default; edit or sync explicitly because it contains production settings and secrets. |
| Provider API key pools | `/etc/onr/keys.yaml` | Preserve and edit only on the server; never replace from the repository by default. |
| Public model routing | `/etc/onr/models.yaml` | Sync from `/home/open-next-router/models.yaml`. |
| Public pricing | `/etc/onr/price.public.yaml` | Sync from `/home/open-next-router/config/price.public.yaml`. |
| Pricing overrides | Path from `pricing.overrides_file` in `/etc/onr/onr.yaml` (usually `/etc/onr/price_overrides.yaml`) | Preserve production overrides; sync explicitly only when the override file is intentionally changed. |
| Provider DSL entry point | `/home/open-next-router/config/onr.conf` | Updated in place with the source checkout. |
| Provider and mode DSL | `/home/open-next-router/config/{providers,modes}/*.conf` | Updated in place with the source checkout. |
| Model cards for the web UI | `/home/open-next-router/config/models.catalog.yaml` | Updated in place with the source checkout. |
| OAuth runtime state | `/var/lib/onr/run/oauth` | Runtime data; do not overwrite during an update. |
| Redis data | Redis DB and prefix configured in `/etc/onr/onr.yaml` | Persistent state; do not flush or replace during a normal update. |
| systemd units | `/etc/systemd/system/{onr,onr-admin-web}.service` | Run `systemctl daemon-reload` after editing. |
| Nginx site | `/etc/nginx/sites-available/onr` | Validate with `nginx -t`, then reload Nginx. |

Relative provider paths work because both services use
`WorkingDirectory=/home/open-next-router`. Production paths in
`/etc/onr/onr.yaml` should otherwise be absolute as shown in `START.md`.

## Recommended update workflow

Inspect and back up the deployment before updating:

```bash
cd /home/open-next-router
git status --short
systemctl status onr.service onr-admin-web.service --no-pager
```

Update the reviewed branch or release:

```bash
git fetch origin
git pull --ff-only origin main
git submodule update --init --recursive
```

Run the deployment helper:

```bash
/home/open-next-router/scripts/update_server.sh --run-tests
```

The helper performs these operations:

1. Creates a private backup under `/root/onr-backups/`.
2. Preserves `/etc/onr/onr.yaml` and `/etc/onr/keys.yaml`.
3. Syncs public model routing and pricing into `/etc/onr`.
4. Builds both binaries and validates the production configuration with the new binaries.
5. Atomically replaces both installed binaries.
6. Restarts the gateway and admin web services and verifies that they are active.

Omit `--run-tests` for a faster update when the exact artifact was already tested
in CI:

```bash
/home/open-next-router/scripts/update_server.sh
```

## Update commands by changed file

| Changed files | Required production action |
| --- | --- |
| Go files, `go.mod`, `go.sum`, embedded admin HTML/CSS/JS | Build and install binaries, validate, restart both services. Run the default helper command. |
| `config/providers/*.conf`, `config/modes/*.conf`, `config/onr.conf` | Validate DSL and reload the gateway. `--skip-build --no-sync-public-config` is sufficient. |
| `models.yaml` | Copy to `/etc/onr/models.yaml`, validate, reload gateway, restart admin web. Use `--skip-build`. |
| `config/price.public.yaml` | Copy to `/etc/onr/price.public.yaml`, validate, reload gateway, restart admin web. Use `--skip-build`. |
| `price_overrides.yaml` | Copy to the path configured by `pricing.overrides_file`, validate, reload gateway, restart admin web. Do not overwrite production overrides accidentally. |
| `config/models.catalog.yaml` | No `/etc` copy is needed; it remains under the source checkout. Restart admin web if an already-running page or process must refresh immediately. |
| `/etc/onr/keys.yaml` | Edit the production file directly, validate, reload gateway, restart admin web so its cached key-pool names are refreshed. |
| `/etc/onr/onr.yaml` | Edit directly and restart both services. A reload is insufficient for changes to enabled flags, paths, Redis settings, listeners, or admin tokens. |
| systemd unit files | `systemctl daemon-reload`, then restart the affected service. |
| Nginx configuration | `nginx -t`, then `systemctl reload nginx`. No ONR rebuild is needed. |
| Documentation or test scripts only | No service action is required. |

### Configuration-only update

After changing provider DSL, `models.yaml`, or public pricing:

```bash
/home/open-next-router/scripts/update_server.sh --skip-build
```

This syncs `models.yaml` and public pricing, validates the deployed
configuration, reloads the gateway, and restarts the admin service.

For a DSL-only change that must not sync public models or prices:

```bash
/home/open-next-router/scripts/update_server.sh \
  --skip-build \
  --no-sync-public-config
```

### Code-only update

If production models and pricing intentionally differ from the checkout:

```bash
/home/open-next-router/scripts/update_server.sh \
  --no-sync-public-config \
  --run-tests
```

### Main configuration update

The recommended approach is to edit `/etc/onr/onr.yaml` directly and run:

```bash
/home/open-next-router/scripts/update_server.sh \
  --skip-build \
  --no-sync-public-config
```

If `/home/open-next-router/onr.yaml` has deliberately been prepared as the
production file, it can be copied explicitly:

```bash
/home/open-next-router/scripts/update_server.sh \
  --skip-build \
  --no-sync-public-config \
  --sync-runtime-config
```

`--sync-runtime-config` overwrites `/etc/onr/onr.yaml`. Review the source file
carefully first; it may contain development ports, Redis prefixes, or secrets.
The helper never copies `keys.yaml` into `/etc/onr`.

### Install without restarting

For a controlled maintenance window:

```bash
/home/open-next-router/scripts/update_server.sh --no-restart
```

The files are installed and validated, but the running processes continue using
the previous binaries/configuration until manually restarted.

## Manual validation

These commands validate the production files without updating them:

```bash
cd /home/open-next-router
/usr/local/bin/onr-admin validate all --config /etc/onr/onr.yaml
/usr/local/bin/onr -t -c /etc/onr/onr.yaml
```

After an update:

```bash
systemctl status onr.service onr-admin-web.service --no-pager
journalctl -u onr.service -u onr-admin-web.service -n 100 -l --no-pager
curl -fsSI http://127.0.0.1:3310/
```

Use a real ONR Access Key without printing it:

```bash
read -rsp 'ONR Access Key: ' ONR_TEST_ACCESS_KEY
echo
export ONR_TEST_ACCESS_KEY
/home/open-next-router/scripts/test_onr_models.py \
  --onr-base-url http://127.0.0.1:3300 \
  --model deepseek-v4-flash
unset ONR_TEST_ACCESS_KEY
```

## Rollback

Every helper run prints its backup directory. Restore binaries from that exact
directory if the new release fails:

```bash
BACKUP_DIR=/root/onr-backups/REPLACE_WITH_EXACT_DIRECTORY
systemctl stop onr-admin-web.service onr.service
install -o root -g root -m 0755 "$BACKUP_DIR/onr" /usr/local/bin/onr
install -o root -g root -m 0755 "$BACKUP_DIR/onr-admin" /usr/local/bin/onr-admin
systemctl start onr.service onr-admin-web.service
```

Restore an individual configuration file only after reviewing the differences:

```bash
diff -u /etc/onr/models.yaml "$BACKUP_DIR/models.yaml"
install -o root -g root -m 0644 "$BACKUP_DIR/models.yaml" /etc/onr/models.yaml
```

Do not delete Redis data or change `key_prefix`, `billing_stream`, or
`access_key_hash_secret` as part of a routine rollback.
