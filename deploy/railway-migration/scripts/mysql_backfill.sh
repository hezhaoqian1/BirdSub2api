# Runs in a NEW MySQL container (nohup) AFTER the new app is live. Fills one big log table from the
# old MySQL without blocking the app:
#   - --skip-add-locks: no LOCK TABLES ... WRITE (10-29: that lock exhausted coincoin's DB pool for 20 min)
#   - one day per mysqldump, so every INSERT batch only takes row locks briefly
# 1) rows with COLUMN >= SINCE: REPLACE (they may have changed after the precopy snapshot)
# 2) older rows day by day: INSERT IGNORE (whatever the precopy already loaded is skipped)
# usage: sh mysql_backfill.sh <old_host> <old_port> <table> <column> "<SINCE utc>"
SRC_H=$1; SRC_P=$2; T=$3; C=$4; SINCE=$5
export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
SRC="-h $SRC_H -P $SRC_P -uroot"
OPTS="--single-transaction --quick --set-gtid-purged=OFF --no-tablespaces --no-create-info --skip-add-locks --skip-triggers"
L=/tmp/backfill_$T.log
echo "$(date -u +%T) start since=$SINCE" > "$L"
mysqldump $SRC $OPTS --replace --where="$C >= '$SINCE'" railway "$T" 2>>"$L" | mysql -uroot railway 2>>"$L"
echo "$(date -u +%T) recent window rc=$?" >> "$L"
D=$(mysql $SRC -N -e "SELECT DATE(MIN($C)) FROM railway.\`$T\`")
END=$(echo "$SINCE" | cut -c1-10)
while [ "$(expr "$D" \<= "$END")" = 1 ]; do
  N=$(date -u -d "$D + 1 day" +%F)
  mysqldump $SRC $OPTS --insert-ignore --where="$C >= '$D' AND $C < '$N' AND $C < '$SINCE'" railway "$T" 2>>"$L" | mysql -uroot railway 2>>"$L"
  echo "$(date -u +%T) $D rc=$?" >> "$L"
  D=$N
  sleep 1
done
echo "done $(date -u +%FT%TZ)" > "/tmp/backfill_$T.done"
