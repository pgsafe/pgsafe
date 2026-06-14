#!/bin/sh
set -e

if [ "${RUN_MODE:-job}" = "cron" ]; then
    printf '%s /usr/local/bin/pgsafe\n' "${CRON_SCHEDULE}" > /tmp/pgsafe.crontab
    exec supercronic /tmp/pgsafe.crontab
fi

exec /usr/local/bin/pgsafe
