/def -i -t'p *' -p1 lo = /echo lo
/def -i -t'p *' -p5 hi = /echo hi
/def -i -t'p *' -p3 -F mid = /echo mid
/def -i -t'q *' -p2 -F qf1 = /echo qf1
/def -i -t'q *' -p2 qn1 = /echo qn1
/def -i -t'q *' -p1 qlow = /echo qlow
/def -i -t'r *' -n2 shots = /echo shot
/def -i -t'r *' -p0 r0 = /echo r0
/def -i -t's *' -c0 never = /echo never
/def -i -t's *' -p1 -E(1==1) condyes = /echo condyes
/def -i -t't *' -p9 -E(1==2) condno = /echo condno
/def -i -t't *' -p1 tlow = /echo tlow
---
/trigger p x

/trigger q x

/trigger r x

/trigger r x

/trigger r x

/trigger s x

/trigger t x
