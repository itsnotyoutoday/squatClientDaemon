/def -i ret = /return 42
/def -i ret2 = /return
/def -i ret3 = /return $[1+2]
/def -i retstr = /return "hello"
/def -i early = /echo a %; /return 1 %; /echo b
/def -i nest = /echo n1 %; /ret %; /echo got %?
/def -i inc = /return $[%1 + 1]
---
/echo [$[ret()]]

/echo [$[ret3()]]

/echo [$[retstr()]]

/echo [$[early()]]

/nest

/echo [$[inc(5)]] [$[inc(inc(1))]]

