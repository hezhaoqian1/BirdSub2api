# Runs in the OLD Postgres container. Allows remote replication connections and creates the
# physical slot the new standby streams from. Reload only - no restart.
set -e
HBA="$PGDATA/pg_hba.conf"
# keep the backup OUTSIDE $PGDATA: pg_basebackup copies all of PGDATA and fails on root-owned files
[ -f /var/lib/postgresql/data/pg_hba.conf.bak-mig ] || cp "$HBA" /var/lib/postgresql/data/pg_hba.conf.bak-mig
# the existing "host all all all scram-sha-256" line does NOT match replication connections
grep -q "# mig-newacct" "$HBA" || echo "host replication postgres all scram-sha-256  # mig-newacct" >> "$HBA"
psql -h localhost -U "$PGUSER" -d "$PGDATABASE" -v ON_ERROR_STOP=1 -At \
  -c "SELECT pg_reload_conf()" \
  -c "SELECT pg_create_physical_replication_slot('mig_newacct', true) WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name='mig_newacct')" \
  -c "SELECT 'slot', slot_name, active FROM pg_replication_slots" \
  -c "SELECT 'wal_level', current_setting('wal_level')" \
  -c "SELECT 'hba_replication_rules', count(*) FROM pg_hba_file_rules WHERE 'replication' = ANY(database) AND address = 'all'"
