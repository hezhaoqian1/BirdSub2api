# Runs in a NEW MySQL container (nohup). Full logical copy of the `railway` schema from the old
# MySQL's TCP proxy. Writes the snapshot start time to /tmp/precopy.t0 and the result to /tmp/precopy.done.
# usage: sh mysql_precopy.sh <old_tcp_host> <old_tcp_port>
SRC_H=$1; SRC_P=$2
export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
mysql -h "$SRC_H" -P "$SRC_P" -uroot -N -e "SELECT UTC_TIMESTAMP()" > /tmp/precopy.t0
S=$(date +%s)
mysqldump -h "$SRC_H" -P "$SRC_P" -uroot --single-transaction --quick --routines --triggers --events \
  --set-gtid-purged=OFF --no-tablespaces --skip-add-locks --databases railway 2>/tmp/precopy.dump.err \
  | mysql -uroot 2>/tmp/precopy.load.err
echo "rc=$? secs=$(( $(date +%s) - S ))" > /tmp/precopy.done
