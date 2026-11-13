; extensions in pkg/tf that real tf rejects (expected-tf shows %ERR)

/echo $[1 && 1]

/echo $[0 || 1]

/echo $[0x10 + 1]

/echo $["a" . "b"]

/echo $[5 % 2]

/set x=1

/echo $[x++]

/echo $[x--]

