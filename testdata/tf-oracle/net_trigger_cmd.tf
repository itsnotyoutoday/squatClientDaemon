;@net
/def -i -t'abc *' -p2 tc1 = /echo tc1 [%*]
/def -i -t'abc d' tc2 = /echo tc2 [%*]
/def -i -t'zzz' -n2 tc3 = /echo tc3
---
/trigger abc x

/trigger abc d

/trigger zzz

/trigger zzz

/trigger zzz

/trigger nomatch
