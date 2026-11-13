/set foo=bar

/set n=5

/echo [%foo] [%{foo}] [%{foo}x] [%foox] [%{foo-dflt}] [%{nosuch-dflt}] [%n]

/echo [$[foo]] [$[n + 1]] [$[strlen(foo)]]

/echo [%{n}0] [$[n]0] [%% n] [100%%]

/echo a%;/echo b

/echo [%;] x

/echo [%P1]

/set e=

/echo [%e] [%{e-dflt}] [$[e]] [%{e}]

/set s=a b  c

/echo [%s] [$[s]] [$[strlen(s)]]

/set  sp = x y 

/echo [%sp] [$[strlen(sp)]]

/set eq=a=b

/echo [%eq]
