/trig foo * = /echo trig1 [%*]
/trigp 5 bar * = /echo trig5 [%*]
/trigp 2 bar x = /echo trig2
/trigc 100 baz = /echo trigc
/trigpc 7 100 qux = /echo trigpc
/trigc 0 never = /echo never
/gag gagpat
/hilite hilpat = /echo hil
/hook ACTIVITY = /echo hookbody
/hook CONFAIL a* = /echo hookbody2 [%*]
---
/trigger foo one two

/trigger bar x

/trigger bar y

/trigger baz

/trigger qux

/trigger never

/trigger gagpat

/trigger hilpat

/untrig foo *

/trigger foo one two

/trigger -n bar x

/trigger -n bar y
