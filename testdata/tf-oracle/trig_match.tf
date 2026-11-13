/def -i -mregexp -t'^(\w+) says (.*)$' r1 = /echo r1 [%P0] [%P1] [%P2] [%1] [%2] [%#]
/def -i -mregexp -t'ab+c' r2 = /echo r2 [%P0] [%PL] [%PR]
/def -i -msimple -t'exact line' s1 = /echo s1 [%*]
/def -i -msubstr -t'needle' s2 = /echo s2 [%*]
/def -i -mregexp -t'UPPER' r3 = /echo r3 [%P0]
/def -i -mregexp -t'lower' r4 = /echo r4 [%P0]
/def -i -mregexp -t'^x(y)?z$' r5 = /echo r5 [%P0] [%P1] [%P9]
---
/trigger Bob says hello there

/trigger xxabbbcyy

/trigger exact line

/trigger Exact line

/trigger exact line more

/trigger hay needle stack

/trigger upper

/trigger UPPER

/trigger LOWER

/trigger xyz

/trigger xz
