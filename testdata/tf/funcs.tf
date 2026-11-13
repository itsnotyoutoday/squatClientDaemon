/echo [$[strlen("hello")]] [$[strcat("a", "b", 3)]] [$[strcmp("a", "b")]] [$[strcmp("b", "a")]] [$[strcmp("a","a")]] [$[strncmp("abcd", "abxx", 2)]]
/echo [$[strchr("hello", "l")]] [$[strrchr("hello", "l")]] [$[strstr("hello", "llo")]] [$[strstr("hello", "z")]]
/echo [$[strrep("ab", 3)]] [$[substr("hello", 1, 3)]] [$[substr("hello", 2)]] [$[substr("hi", 5)]]
/echo [$[pad("ab", 5)]] [$[pad("ab", -5)]]| [$[pad("a", 3, "b", -3)]]|
/echo [$[tolower("AbC")]] [$[toupper("AbC")]] [$[replace("-", "+", "a-b-c")]]
/echo [$[ascii("A")]] [$[char(66)]] [$[char(955)]]
/echo [$[abs(-3)]] [$[abs(-2.5)]] [$[trunc(3.9)]] [$[mod(7, 3)]] [$[pow(2, 10)]] [$[sqrt(16)]] [$[exp(0)]] [$[ln(1)]] [$[log10(1000)]]
/echo [$[sin(0)]] [$[cos(0)]] [$[atan(0)]]
/echo [$[rand(10)]] [$[whatis(1)]] [$[whatis(1.5)]] [$[whatis("x")]]
/echo [$[time()]] [$[ftime("%Y-%m-%d %H:%M:%S", 0)]] [$[mktime(2000, 1, 1, 0, 0, 0)]]
/echo [$[ftime("%a %b %e %j %p %I", 86400)]]
/echo [$[test("1+2")]] [$[eval("/echo hi")]]
/echo [$[regmatch("(\\d+)-(\\d+)", "abc 12-34 def")]] [%P0] [%P1] [%P2] [%L] [%R]
/echo [$[regmatch("zzz", "abc")]] [%P1]
/echo [$[columns()]] [$[lines()]] [$[is_connected()]] [$[nactive()]] [$[world_info("name")]]
/echo [$[strip_attr(encode_attr("hi@there", "Cred"))]]
/echo [$[encode_attr("hi", "Cred,u")]]
/def opts = /let rest=$[getopts("ab:c", {*})] %; /echo a=[%{opt_a}] b=[%{opt_b}] c=[%{opt_c}] rest=[%rest]
/opts -a -b val -- x y
/opts -ab2 foo
/opts plain
/echo [$[nosuchfn(1)]]
/echo [$[strlen()]]
