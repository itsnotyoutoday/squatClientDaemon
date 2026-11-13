/def -i sa = /echo [%0] [%1] [%2] [%3] [%*] [%#] [%{1}] [%{#}] [%{*}]
/def -i sb = /echo [%-1] [%{-1}] [%{1-}] [%L] [%{L}] [%{-L}] [%L2] [%{L3}]
/def -i sc = /echo [%{9}] [%9] [%{3-x}] [%{1-x}] [%{nosuch-dflt}] [%{nosuch}] [%nosuch]
/def -i sd = /echo [%1%2] [%1 %2] [%%] [%{1}x] [%1x] [%P0]
/def -i se = /shift %; /echo [%1] [%#] [%*] %; /shift 2 %; /echo [%1] [%#] [%*]
---
/sa

/sa a

/sa a b c d

/sa  a   b

/sb x y z

/sb

/sc a b

/sd 1 2

/se a b c d e
