/set g=global
/def -i sc1 = /let g=local %; /echo [%g] [$[g]]
/def -i sc2 = /set g=changed %; /echo [%g]
/def -i sc3 = /let l=1 %; /echo [%l] %; /sc4 %; /echo [%l]
/def -i sc4 = /echo [%{l-none}] %; /let l=2
/def -i sc5 = /let n=5 %; /set n=6 %; /echo [%n] [$[n]]
/def -i sc6 = /test z := 9 %; /echo [%z]
/def -i sc7 = /let q=1 %; /test q := 2 %; /echo [%q]
/def -i sc8 = /let a=1 %; /let a=2 %; /echo [%a]
/def -i sc9 = /let t=$[1+2] %; /echo [%t] [$[t * 2]]
/def -i sc10 = /let e= %; /echo [%e]
---
/sc1

/echo [%g]

/sc2

/echo [%g]

/sc3

/sc5

/echo [%n]

/sc6

/echo [%z]

/sc7

/sc8

/sc9

/sc10

/set one=1 %; /set two=2 %; /echo [%one%two]

/set sp=a b c %; /echo [%sp]

/unset sp %; /echo [%{sp-gone}] [$[sp]]

/set cnt=1 %; /echo [%{cnt}] [$[cnt + 1]]

/set u=5 %; /unset u %; /unset u
