#!/bin/bash
# Start command of the NEW Postgres service (shipped base64 in the MIG_BOOT_B64 variable):
#   /bin/bash -c "echo $MIG_BOOT_B64 | base64 -d > /tmp/boot.sh && exec bash /tmp/boot.sh"
# Empty volume -> pg_basebackup the old primary through its TCP proxy and come up as a streaming
# standby (-R writes standby.signal + primary_conninfo). Existing cluster -> start normally.
# Either way the image's own wrapper.sh does the real start (certs, pg_hba heal, etc.).
set -e
D=/var/lib/postgresql/data
if [ ! -f "$PGDATA/PG_VERSION" ]; then
  echo "mig: no cluster yet -> pg_basebackup from $MIG_SRC_HOST:$MIG_SRC_PORT slot=$MIG_SLOT"
  # back up into a temp dir and move it into place only when complete: PG_VERSION is written early,
  # so a half-finished backup in $PGDATA would look like a valid cluster after a restart
  rm -rf "$D/bb-tmp"; mkdir -p "$D/bb-tmp"; chown -R postgres:postgres "$D"; chmod 700 "$D/bb-tmp"
  gosu postgres env PGPASSWORD="$MIG_SRC_PASSWORD" PGSSLMODE=require pg_basebackup \
    -h "$MIG_SRC_HOST" -p "$MIG_SRC_PORT" -U postgres -D "$D/bb-tmp" -R -X stream -S "$MIG_SLOT" -v -c fast
  rm -rf "$PGDATA"; mv "$D/bb-tmp" "$PGDATA"
  echo "mig: basebackup done"
fi
exec /usr/local/bin/wrapper.sh postgres -p 5432 -c listen_addresses=*
