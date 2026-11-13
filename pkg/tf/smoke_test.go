package tf

import "testing"

func TestSmoke(t *testing.T) {
	i, h := newTest()
	i.Exec("w", "/set x=5")
	i.Exec("w", "/echo x is %{x} and $[x*2+1]")
	i.Exec("w", "hello")
	i.Exec("w", "/def foo = /echo foo %1 %* %; /echo done")
	i.Exec("w", "/foo a b c")
	i.Exec("w", "/if (x > 3) /echo big %; /else /echo small %; /endif")
	i.Exec("w", "/nosuch")
	t.Log("\n" + h.dump())
}
