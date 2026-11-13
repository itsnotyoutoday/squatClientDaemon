/echo r $[1 & 0] $[1 | 0] $[3 & 4] $[0 | 5] $[5 | 0] $[0 & 5] $[2 & 3]

/set one=1

/set zero=0

/set empty=

/set str=a

/echo r $[one ? "t" : "f"] $[zero ? "t" : "f"] $[empty ? "t" : "f"] $[str ? "t" : "f"] $[zero ? "t" : "f"]

/echo r $[one ? 5 : 6] $[zero ? 5 : 6] $[one ? one : zero] $[zero ? : 9]

/echo r $[!"" ] $[!"a"] $[!!5] $[!0.0]

/set x=0

/echo r $[x & (y := 7)] $[y]

/echo r $[1 | (z := 7)] $[z]

/echo r $[1 & 2 | 0] $[0 | 0 & 1]

