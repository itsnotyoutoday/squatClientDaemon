/def -t'You say, "*"' say_trig = /echo heard: %P1 words=%# first=%1
/def -p5 -t'* arrives.' high = /echo high priority: %1
/def -p1 -t'* arrives.' low = /echo low priority: %1
/def -F -p9 -t'Bob arrives.' fall = /echo fall-through first
/def -n2 -t'ping' shots = /echo pong
/def -mregexp -t'^(\w+) tells you: (.*)$' tell = /echo tell from %P1 saying [%P2] left=[%L] right=[%R]
/def -msimple -t'exact line' simple = /echo simple matched
/def -t'secret*' -g hider = /echo hidden
/def -t'*dragon*' -ahCred hl
/def -mregexp -t'(?P<a>\d+) gold' -P1BCblue part
/def -t'only*' -wother wonly = /echo world-only
/def -t'maybe' -c50 -E'x == 1' cond = /echo cond-fired
/set x=0
;@line main You say, "hello there"
;@line main Alice arrives.
;@line main Bob arrives.
;@line main ping
;@line main ping
;@line main ping
;@line main Zed tells you: meet me at 5
;@line main exact line
;@line main exact line plus
;@line main secret stuff
;@line main a dragon appears
;@line main you find 25 gold here
;@line main only here
;@line other only here
;@line main maybe
/set x=1
;@line main maybe
