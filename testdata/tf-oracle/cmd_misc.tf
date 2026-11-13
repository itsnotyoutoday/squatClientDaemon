/def -i sh1 = /echo [%1] [%#] %; /shift %; /echo [%1] [%#] [%*] %; /shift 2 %; /echo [%1] [%#] [%*]
/def -i ev1 = /eval /echo [%1] %%1 %; /eval /echo $[%1 + 1]
/def -i tt = /test $[%1] %; /echo [%?]
/def -i ec = /echo a%%b
/def -i nm = /echo [%0] [%1]
---
/sh1 a b c d

/ev1 5

/nosuchcmd arg

/nm x

/eval /echo one %; /echo two

/eval /nm y

/set v=zzz

/test v =~ "zzz"

/echo [$[v =~ "zzz" ? "same" : "diff"]]

/echo [%{v}] [%{v-d}] [%{nov-d}] [%{nov}]

/echo before %; /nosuchcmd %; /echo after

/echo   trailing   

/def -i

/undef

/echo [$[strlen(%{v})]]
