/echo r $["abc" =/ "a*"] $["abc" =/ "a"] $["abc" =/ "*"] $["" =/ ""] $["a b" =/ "a*"]

/echo r $["hello world" =/ "hello*"] $["hello" !/ "h?llo"] $["foo" =/ "{foo|bar}"] $["hello" =/ "h[a-f]llo"] $["hello" =/ "h[e-f]llo"]

/echo r $["Hello" =/ "hello"] $["Hello" =/ "H*o"] $["abc" =/ "a?c"] $["abc" =/ "a??c"]

/echo r $["foo.bar" =/ "foo.*"] $["a*b" =/ "a\\*b"] $["axb" =/ "a\\*b"]

/echo r $["abc" =/ "[a-c]*"] $["abc" =/ "[!a-c]*"] $["xbc" =/ "[^a-c]*"]

/echo r $["abc" =/ "{a|b}*"]

