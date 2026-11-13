; examples from docs/TF-LANGUAGE.md section 12
/def -hCONNECT on_connect = /repeat -300 2 /send IDLE
/def -mregexp -t'^(\w+) tells you: (.*)$' -P1Cbrightgreen,h tell = \
    /notify Tell from %P1 | %P2
/def -wmain -t'\[Spam\]*' -g spam
/def -t'* is dead.' -E'looting' -c50 loot = get all from corpse
/def -t'*ALERT*' -a'BC#400000,C#ffe0e0,h' alert
/def max = /return {1} > {2} ? {1} : {2}
/echo biggest: $[max(3, 9)] $[max(9, 3)]
/def count = /let i=1 %; /while (i <= {1}) %; /echo %i %; /let i=$[i+1] %; /done
/count 3
;@hook main CONNECT main
;@advance 300s
;@advance 300s
;@advance 300s
;@line main Zed tells you: hi there
;@line main [Spam] buy now
;@line other [Spam] buy now
;@line main the orc is dead.
/set looting=1
;@line main the orc is dead.
;@line main *ALERT* now
