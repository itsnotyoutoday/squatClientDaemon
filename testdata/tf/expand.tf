; substitution basics
/set name=World
/echo Hello, %{name}! %name %{name}s
/echo percent: 100%% done
/echo semi%;/echo second
/set n=3
/echo expr: $[n * 2 + 1] $[n > 2 ? "yes" : "no"] $[strlen("héllo")]
/echo unset:[%{nothing}] set?%{?name} %{?nothing}
/echo dollar: $$5 $notexpr
/def show = /echo args=[%*] n=%# 1=[%1] 2=[%2] 3=[%3] 0=%0
/show alpha beta
/show
/show one two three four
/eval /echo nested %%{name}
/set cmd=/echo from-var
/eval %cmd
/echo cmdsub: $(/echo inner) and $[strcat("a","b")]
/def r = /return 42
/echo return: $(/r)
/echo fn: $[r()]
/def shifty = /shift %; /echo after shift: %* (%#)
/shifty a b c
