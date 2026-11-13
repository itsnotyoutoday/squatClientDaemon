/repeat -3600 3 /echo hello
/repeat -0:30 1 /echo dinner
---
/ps

/ps -s

/ps -r

/ps -q

/echo [$[repeat("-3600 1 /echo q")]]

/kill 99

/kill 1

/ps

/repeat -S 2 /echo sync

/repeat 2 /echo nosecs

/repeat -3600 i /echo forever

/ps

/repeat

/kill 2 3

/ps -s
