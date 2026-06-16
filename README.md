# pgsafe

Containerised PostgreSQL backup tool. Dumps one or more databases with `pg_dump`, streams through optional compression and encryption, and uploads to any S3-compatible storage.

## Quick start

### One-shot job (Kubernetes CronJob, CI, etc.)

```sh
docker run --rm \
  -e SINGLE_MYDB_DATABASE_URL="postgres://user:pass@host:5432/mydb" \
  -e S3_ENDPOINT="https://s3.amazonaws.com" \
  -e S3_BUCKET="my-backups" \
  -e S3_REGION="us-east-1" \
  -e S3_ACCESS_KEY_ID="AKIAIOSFODNN7EXAMPLE" \
  -e S3_SECRET_ACCESS_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" \
  ghcr.io/pgsafe/pgsafe:1
```

### Persistent cron container

```sh
docker run -d \
  -e RUN_MODE=cron \
  -e CRON_SCHEDULE="0 2 * * *" \
  -e SINGLE_MYDB_DATABASE_URL="postgres://user:pass@host:5432/mydb" \
  -e S3_ENDPOINT="https://s3.amazonaws.com" \
  -e S3_BUCKET="my-backups" \
  -e S3_REGION="us-east-1" \
  -e S3_ACCESS_KEY_ID="AKIAIOSFODNN7EXAMPLE" \
  -e S3_SECRET_ACCESS_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" \
  ghcr.io/pgsafe/pgsafe:1
```

## Environment variables

### Database connections

At least one of the following must be set. `CONNNAME` must match `[A-Z0-9]+`.

| Variable | Description |
|---|---|
| `MULTI_<CONNNAME>_DATABASE_URL` | Connect to this PostgreSQL instance and back up **all** databases found in `pg_database` (excluding templates). The URL's database path is used only for the initial connection. |
| `MULTI_<CONNNAME>_DATABASE_NAMES_GLOB` | Optional glob pattern (e.g. `app_*`) to filter which databases are backed up for the matching `MULTI_*` connection. Uses standard glob syntax (`*`, `?`, `[abc]`). Mutually exclusive with `DATABASE_NAMES_REGEX`. |
| `MULTI_<CONNNAME>_DATABASE_NAMES_REGEX` | Optional regular expression (e.g. `^app_`) to filter which databases are backed up. Full Go `regexp` syntax. Mutually exclusive with `DATABASE_NAMES_GLOB`. |
| `SINGLE_<CONNNAME>_DATABASE_URL` | Back up **exactly the one** database named in the URL path. |

Multiple variables of either type can be set simultaneously; pgsafe will back up all of them.

**Examples:**

```sh
# Back up every database on two different instances
MULTI_PROD_DATABASE_URL=postgres://admin:s3cr3t@prod-pg:5432/postgres
MULTI_STAGING_DATABASE_URL=postgres://admin:s3cr3t@staging-pg:5432/postgres

# Back up only databases whose names start with "app_"
MULTI_PROD_DATABASE_URL=postgres://admin:s3cr3t@prod-pg:5432/postgres
MULTI_PROD_DATABASE_NAMES_GLOB=app_*

# Back up a single specific database
SINGLE_ANALYTICS_DATABASE_URL=postgres://ro:pass@pg:5432/analytics
```

### S3 / object storage

| Variable | Required | Default | Description |
|---|:---:|---|---|
| `S3_ENDPOINT` | yes | — | Base URL of the S3 endpoint, e.g. `https://s3.amazonaws.com` or `https://<account>.r2.cloudflarestorage.com` |
| `S3_BUCKET` | yes | — | Destination bucket name |
| `S3_REGION` | yes | — | AWS region or equivalent, e.g. `us-east-1`, `auto` |
| `S3_ACCESS_KEY_ID` | yes | — | Access key ID |
| `S3_SECRET_ACCESS_KEY` | yes | — | Secret access key |
| `S3_PATH_STYLE` | no | `false` | Set `true` for providers that require path-style URLs (MinIO, some self-hosted). |
| `S3_MULTIPART_PART_SIZE` | no | `64` | Multipart upload part size in MB (minimum `5`). Increase to reduce the number of S3 API operations. |

Objects are uploaded to `<CONNNAME>/<dbname>/<timestamp>.<ext>` inside the bucket.

### Run mode

| Variable | Default | Description |
|---|---|---|
| `RUN_MODE` | `job` | `job` — run once and exit (for k8s CronJob, CI). `cron` — stay running and use supercronic to execute on a schedule. |
| `CRON_SCHEDULE` | `0 0 * * *` | Standard cron expression used when `RUN_MODE=cron`. |

### Dump settings

| Variable | Default | Description |
|---|---|---|
| `DUMP_FORMAT` | `custom` | Output format: `custom` (`-Fc`, pg_dump archive), `plain` (SQL text), or `tar` (`-Ft`, tar archive). All formats are streamed directly to S3 without a temporary file. |
| `DUMP_JOBS` | `1` | Parallel dump workers passed as `--jobs` to pg_dump. Only used when `DUMP_FORMAT=tar`. |

### Processing

| Variable | Default | Description |
|---|---|---|
| `COMPRESSION_METHOD` | `none` | Compress the dump before upload. One of: `none`, `gzip`, `bzip2`, `xz`. Applied in-memory/as-stream — no extra disk space needed. |
| `ENCRYPTION_CIPHER_KEY` | — | Passphrase for AES-256-CBC encryption (via OpenSSL). When set, the upload is encrypted in-stream. Appends `.enc` to the S3 object key. |
| `ENCRYPTION_ITERATIONS` | `100000` | PBKDF2 iteration count passed to `openssl enc -iter`. Higher values increase brute-force resistance at the cost of encrypt/decrypt time. Only relevant when `ENCRYPTION_CIPHER_KEY` is set. |

### Notifications

All notification channels share the same delivery guarantees:

- **Asynchronous** — notifications never block the backup process.
- **Independent** — a failure or hang on one channel does not affect any other.
- **Retried** — up to 10 delivery attempts per notification, with exponential backoff starting at 2 s and capped at 30 s (~3 minutes of total wait time before giving up).

In job mode (`RUN_MODE=job`) pgsafe waits up to 5 minutes after the backup completes for all in-flight notifications to be delivered before the process exits.

#### Webhook

| Variable | Description |
|---|---|
| `WEBHOOK_SUCCESS_URL` | URL to `POST` when **all** backups in a run succeed. Body: `{ "startedAt": "<ISO8601>", "finishedAt": "<ISO8601>", "databases": [ { "connName": "PROD", "dbName": "mydb", "mode": "single\|multi", "url": "user@host:port/db", "sizeBytes": 1234, "duration": "1m2s" } ] }`. Passwords are never included in the database URLs. |
| `WEBHOOK_FAILURE_URL` | URL to `POST` when **any** backup in a run fails. Body: `{ "startedAt": "<ISO8601>", "finishedAt": "<ISO8601>", "message": "..." }`. |

#### Slack

| Variable | Default | Description |
|---|---|---|
| `SLACK_WEBHOOK_URL` | — | Incoming webhook URL. When unset, Slack notifications are disabled. |
| `SLACK_EVENTS` | `failure` | Which events trigger a Slack message. One of: `failure`, `success`, `all`. |

#### SMTP

SMTP is enabled by setting `SMTP_HOST`.

| Variable | Default | Description |
|---|---|---|
| `SMTP_HOST` | — | SMTP server hostname. Leave unset to disable email notifications. |
| `SMTP_PORT` | `587` | SMTP server port. |
| `SMTP_TLS_MODE` | `starttls` | Connection security. One of: `none` (plain), `starttls` (STARTTLS upgrade on connect), `tls` (implicit TLS / SMTPS). |
| `SMTP_USERNAME` | — | SMTP authentication username. Leave unset for unauthenticated relays. |
| `SMTP_PASSWORD` | — | SMTP authentication password. |
| `SMTP_FROM_NAME` | — | Display name in the `From` header, e.g. `pgsafe`. |
| `SMTP_FROM_EMAIL` | — | Sender address (required when `SMTP_HOST` is set). |
| `SMTP_TO_EMAIL` | — | Recipient address (required when `SMTP_HOST` is set). |
| `SMTP_EVENTS` | `failure` | Which events trigger an email. One of: `failure`, `success`, `all`. |

#### OpenTelemetry (OTLP)

When `OTEL_OTLP_ENDPOINT` is set, structured log records are exported to the configured OTLP HTTP endpoint in addition to the JSON stdout output. The existing stdout logging is unaffected.

| Variable | Default | Description |
|---|---|---|
| `OTEL_OTLP_ENDPOINT` | — | Base URL of the OTLP HTTP endpoint, e.g. `http://collector:4318`. When unset, OTel export is disabled. |
| `OTEL_OTLP_HEADERS` | — | Comma-separated `Key=Value` pairs sent as HTTP headers on every export request, e.g. `Authorization=Bearer token,X-Dataset=prod`. |
| `OTEL_OTLP_SERVICE_NAME` | `pgsafe` | Value of the `service.name` resource attribute attached to all exported log records. |

## Examples

### Cloudflare R2

R2 requires path-style URLs (`S3_PATH_STYLE=true`). 

```sh
docker run --rm \
  -e SINGLE_PROD_DATABASE_URL="postgres://user:pass@pg:5432/mydb" \
  -e S3_ENDPOINT="https://<account_id>.r2.cloudflarestorage.com" \
  -e S3_BUCKET="pg-backups" \
  -e S3_REGION="auto" \
  -e S3_ACCESS_KEY_ID="<r2_access_key>" \
  -e S3_SECRET_ACCESS_KEY="<r2_secret_key>" \
  -e S3_PATH_STYLE=true \
  ghcr.io/pgsafe/pgsafe:1
```

### AWS S3 with gzip compression and encryption

```sh
docker run --rm \
  -e SINGLE_PROD_DATABASE_URL="postgres://user:pass@pg:5432/mydb" \
  -e S3_ENDPOINT="https://s3.amazonaws.com" \
  -e S3_BUCKET="pg-backups" \
  -e S3_REGION="eu-west-1" \
  -e S3_ACCESS_KEY_ID="AKIAIOSFODNN7EXAMPLE" \
  -e S3_SECRET_ACCESS_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" \
  -e COMPRESSION_METHOD=gzip \
  -e ENCRYPTION_CIPHER_KEY="a-strong-passphrase" \
  ghcr.io/pgsafe/pgsafe:1
```

### Back up all databases on a server, nightly at 02:00

```sh
docker run -d \
  -e RUN_MODE=cron \
  -e CRON_SCHEDULE="0 2 * * *" \
  -e MULTI_PROD_DATABASE_URL="postgres://admin:pass@pg:5432/postgres" \
  -e S3_ENDPOINT="https://s3.amazonaws.com" \
  -e S3_BUCKET="pg-backups" \
  -e S3_REGION="us-east-1" \
  -e S3_ACCESS_KEY_ID="AKIAIOSFODNN7EXAMPLE" \
  -e S3_SECRET_ACCESS_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" \
  ghcr.io/pgsafe/pgsafe:1
```

## Decrypting backups

Encryption is applied last (after compression), so decryption must come first. Use the same passphrase and iteration count that were set during the backup.

**Encrypted only** (no compression, custom format):

```sh
openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 \
  -pass pass:"YOUR_PASSPHRASE" \
  -in PROD_mydb_20260613_020000.dump.enc \
  -out PROD_mydb_20260613_020000.dump
```

**Encrypted + gzip** (plain format):

```sh
openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 \
  -pass pass:"YOUR_PASSPHRASE" \
  -in PROD_mydb_20260613_020000.sql.gz.enc \
| gunzip > PROD_mydb_20260613_020000.sql
```

**Encrypted + bzip2**:

```sh
openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 \
  -pass pass:"YOUR_PASSPHRASE" \
  -in PROD_mydb_20260613_020000.sql.bz2.enc \
| bzip2 -d > PROD_mydb_20260613_020000.sql
```

**Encrypted + xz**:

```sh
openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 \
  -pass pass:"YOUR_PASSPHRASE" \
  -in PROD_mydb_20260613_020000.sql.xz.enc \
| xz -d > PROD_mydb_20260613_020000.sql
```

Replace `100000` with the value of `ENCRYPTION_ITERATIONS` if you changed it from the default.

## Docker image

Images (`linux/amd64`, `linux/arm64`) are published to GHCR:

| Tag | Points to |
|---|---|
| `ghcr.io/pgsafe/pgsafe:latest` | Latest commit on `main` |
| `ghcr.io/pgsafe/pgsafe:1.2.3` | Exact release `v1.2.3` |
| `ghcr.io/pgsafe/pgsafe:1` | Latest `v1.x.x` release |

## License

MIT — see [LICENSE](LICENSE).
