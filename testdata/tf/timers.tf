/repeat -5 3 /echo tick
/ps
;@advance 4s
;@advance 2s
;@advance 5s
;@advance 5s
;@advance 5s
/echo ps now:
/ps
/repeat -10 5 /echo never
/ps
/kill 2
/ps
;@advance 100s
/repeat 2 /echo immediate
/repeat -1:30 1 /echo ninety
;@advance 89s
;@advance 1s
