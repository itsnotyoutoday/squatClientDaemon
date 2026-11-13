/set n=0
/while (n < 3)
  /echo loop %n
  /set n=$[n+1]
/done
/set i=0
/while (1) /do
  /set i=$[i+1]
  /if (i == 2) /continue_not_a_thing %; /endif
  /if (i >= 4) /break %; /endif
/done
/echo i=%i
/if (n == 3) /echo is three %; /elseif (n == 4) /echo is four %; /else /echo other %; /endif
/if (n == 4) /echo is four %; /elseif (n == 3) /echo elseif hit %; /else /echo other %; /endif
/if (n == 9)
  /echo no
/else
  /echo else branch
/endif
/set k=0
/do
  /set k=$[k+1]
/while (k < 5)
/echo k=%k
/for j 1 3 /echo j=%j %; /done
/def nested = /let t=%1 %; /while (t > 0) %; /echo t=%t %; /let t=$[t-1] %; /done %; /echo end
/nested 2
/def lt = /let a=local %; /set g=global %; /echo in:%a
/lt
/echo out:[%{a}] g=%g
/def early = /echo before %; /return %; /echo not reached
/early
/def deep = /deep
/deep
/echo survived
/def inner = /echo inner got %1
/def outer = /inner %1 %; /inner %2
/outer x y
/echo done
/if (1) /echo unclosed-if-in-line
