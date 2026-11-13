/echo r $[y := 10, y + 1] $[y += 5] $[y] $[y *= 2] $[y -= 1] $[y /= 3]

/set x=5

/echo r $[++x] $[x] $[--x] $[x]

/echo r $[a := b := 3] $[a] $[b]

/echo r $[s := "abc"] $[strlen(s)] $[s =~ "abc"]

/echo r $[n := 7] $[n * n] $[(n + 1) * (n - 1)]

/echo r $[f := 1.5] $[f * 2] $[f + 1]

/echo r $[k := 5, k = 6, k]

/echo r $[(c := 2) + c]

/echo r $[u := 1] $[u := u + 1] $[u]

