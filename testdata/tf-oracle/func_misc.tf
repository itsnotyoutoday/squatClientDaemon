/set sv=hello
/set nv=42
/echo r $[whatis(1)] $[whatis(1.5)] $[whatis("a")] $[whatis(sv)] $[whatis(nv)] $[whatis(nosuch)]

/echo r $[whatis(1+1)] $[whatis(1.0*2)] $[whatis("")] $[whatis(strlen("a"))]

/def -i go1 = /let x=$[getopts("ab:c", "")] %; /echo [%x] [%opt_a] [%opt_b] [%opt_c] [%*]

/def -i go2 = /if (getopts("ab:c", "")) /echo ok [%opt_a] [%opt_b] [%opt_c] [%*] %; /else /echo bad %; /endif

/go2 -a

/go2 -a -b val rest

/go2 -c -- -a

/go2 plain words

/go2 -bx -a

/go2 -z
