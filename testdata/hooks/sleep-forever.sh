#!/bin/sh
# sleep-forever.sh
#
# Behaviour: reads (and discards) the hook JSON payload on stdin, spawns a
# child `sleep 300` (in this script's own process group, since it is
# started without setsid/nohup), prints the child's pid to stdout first,
# then waits on it. The hook itself never exits on its own. Used to test
# that harness kills the whole process group when it times out or cancels
# a hook, not just the immediate hook process — a plain `kill <pid>` on the
# shell would leave the `sleep 300` child running. The test can `pgrep`
# using the printed pid (or its process group) to confirm both processes
# died together.
cat >/dev/null
sleep 300 &
child=$!
echo "$child"
wait "$child"
