# Runs in a NEW MySQL container right before cutover. An unfinished precopy must not keep going:
# it recreates tables in alphabetical order and would overwrite later tables with the old snapshot.
for p in /proc/[0-9]*; do
  c=$(tr '\000' ' ' < "$p/cmdline" 2>/dev/null)
  case "$c" in
    "sh /tmp/precopy.sh"*|*"--databases railway"*) kill "${p#/proc/}" && echo "killed ${p#/proc/}: ${c%% -*}" ;;
  esac
done
sleep 2
export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
# the precopy loader connects over the local socket; end its in-flight statement too
for id in $(mysql -uroot -N -e "SELECT id FROM information_schema.processlist WHERE host='localhost' AND command='Query' AND info LIKE 'INSERT INTO%'"); do
  mysql -uroot -e "KILL $id" && echo "killed connection $id"
done
[ -f /tmp/precopy.done ] && echo "precopy had finished: $(cat /tmp/precopy.done)" || echo "precopy was still running (now stopped)"
