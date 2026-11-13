/echo r $[regmatch("b+", "aabbbcc")] [%P0] [%PL] [%PR]

/echo r $[regmatch("(a)(b)(c)", "xabcx")] [%P0] [%P1] [%P2] [%P3] [%P4] [%PL] [%PR]

/echo r $[regmatch("z", "abc")] [%P0]

/echo r $[regmatch("^(\\w+) (\\w+)$", "hello world")] [%P1] [%P2]

/echo r $[regmatch("(x)?y", "y")] [%P0] [%P1]

/echo r $[regmatch("ABC", "abc")] $[regmatch("abc", "ABC")] $[regmatch("(?i)ABC", "abc")]

/echo r $[regmatch("a.c", "a\nc")]

/echo r $[regmatch("", "abc")] [%P0]

/echo r $[regmatch("a|b", "xbx")] [%P0]

/echo r $[regmatch("(a(b))", "ab")] [%P1] [%P2]

/echo r $[regmatch("\\d+", "abc 123 def 456")] [%P0]

/echo r $[regmatch("[a-c]+$", "xyzabc")] [%P0]
