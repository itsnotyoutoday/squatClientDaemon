/set a=1

/set v1=one

/set name=v

/echo [$[strcat("x", a)]] [$[strcat("%{a}", "y")]] [%{name}%{a}]

/echo [$(/echo hi)] [$(/echo a b)]

/echo [$(/echo $[1+2])]

/echo $[$[1+1]]

/echo [$[strlen("$[1]")]]

/eval /echo %{a}

/eval /echo \%{a}

/eval /echo \$[1+1]

/eval /echo $[1+1]

/eval /set b=$[a+1]

/echo [%b]

/eval /echo %%{a}
