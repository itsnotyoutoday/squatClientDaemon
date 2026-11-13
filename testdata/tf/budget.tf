/echo before
/set s=0
/while (1) /set s=$[s+1] %; /done
/echo after-runaway
/def forever = /while (1) %; /let q=1 %; /done
/forever
/echo after-forever
/def rec = /rec
/rec
/echo still-alive
/def big = /let x=a %; /while (1) %; /let x=$[strcat(x, x)] %; /done
/big
/echo after-big
/echo $[strrep("abc", 1000000000)]
/echo after-strrep
