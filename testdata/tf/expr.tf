/test x := 5
/echo $[x + 3] $[x - 3] $[x * 3] $[x / 2] $[x / 2.0] $[-x] $[!x] $[!0]
/echo $[1 + 2 * 3] $[(1 + 2) * 3] $[10 - 2 - 3] $[2 * 3 + 4 * 5]
/echo $[x == 5] $[x != 5] $[x < 6] $[x >= 5] $["abc" == "abc"] $["abc" < "abd"]
/echo $["1.0" == 1] $["1.0" =~ "1"] $["abc" =~ "abc"] $["abc" !~ "abd"]
/echo $["hello world" =/ "hello*"] $["hello" !/ "h?llo"] $["foo" =/ "{foo|bar}"]
/echo $[1 & 0] $[1 | 0] $[1 && 1] $[0 || 0] $[x > 3 & x < 10]
/echo $[y := 10, y + 1] $[y += 5] $[y] $[y *= 2] $[y -= 1] $[y /= 3]
/echo $[x++] $[x] $[++x] $[x--] $[--x]
/echo $[1.5 + 1.5] $[0.1 + 0.2] $[7 / 2]
/echo $[0x10 + 1] $[1e3] $[.5 * 4]
/echo $[x ? "t" : "f"] $[0 ? "t" : "f"]
/echo $["a" + 1] $[strlen(123)]
/echo $[{*}] $[{#}]
/set big=99999999999
/echo $[big * 10]
/echo $[1/0]
/echo after-error
