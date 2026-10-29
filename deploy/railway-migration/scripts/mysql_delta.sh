# Runs in a NEW MySQL container during cutover, while BOTH old and new apps are stopped.
# 1) every table except WINDOW_TABLES/SKIP_TABLES: drop + full copy (they are small)
# 2) WINDOW_TABLES ("table:column ..."): REPLACE rows with column >= SINCE (on top of the precopy)
# SKIP_TABLES (big append-mostly logs) are filled later in the background by mysql_backfill.sh.
# usage: WINDOW_TABLES="t:col ..." SKIP_TABLES="t ..." sh mysql_delta.sh <old_host> <old_port> "<SINCE utc>"
SRC_H=$1; SRC_P=$2; SINCE=$3
: "${WINDOW_TABLES:=}"; : "${SKIP_TABLES:=}"
export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
SRC="-h $SRC_H -P $SRC_P -uroot"
# --skip-add-locks: never emit LOCK TABLES ... WRITE (that is what froze coincoin on 10-29)
OPTS="--single-transaction --quick --set-gtid-purged=OFF --no-tablespaces --skip-add-locks"
ALL=$(mysql $SRC -N -e "SELECT table_name FROM information_schema.tables WHERE table_schema='railway' AND table_type='BASE TABLE'")
WT=""; for w in $WINDOW_TABLES; do WT="$WT ${w%%:*}"; done
FULL=""; for t in $ALL; do case " $WT $SKIP_TABLES " in *" $t "*) ;; *) FULL="$FULL $t" ;; esac; done
S=$(date +%s); : > /tmp/delta.err
mysqldump $SRC $OPTS railway $FULL 2>>/tmp/delta.err | mysql -uroot railway 2>>/tmp/delta.err
echo "full-copy tables: rc=$? t=$(( $(date +%s) - S ))s"
for w in $WINDOW_TABLES; do
  t=${w%%:*}; c=${w#*:}
  mysqldump $SRC $OPTS --no-create-info --replace --skip-triggers --where="$c >= '$SINCE'" railway "$t" 2>>/tmp/delta.err | mysql -uroot railway 2>>/tmp/delta.err
  echo "$t ($c >= $SINCE): rc=$? t=$(( $(date +%s) - S ))s"
done
grep -v "Using a password" /tmp/delta.err | head -5
for t in $ALL; do
  case " $SKIP_TABLES " in *" $t "*) continue ;; esac
  a=$(mysql $SRC -N -e "SELECT COUNT(*) FROM railway.\`$t\`"); b=$(mysql -uroot -N -e "SELECT COUNT(*) FROM railway.\`$t\`")
  [ "$a" != "$b" ] && echo "DIFF $t old=$a new=$b"
done
echo "delta-done tables=$(echo $ALL | wc -w)"
