;@file data.txt
alpha
beta
;@endfile
;@file lib.tf
; a library
/def -i libmacro = /echo from lib %1
/def multi = /echo a \
   b
/echo loading lib
/if (1)
  /echo inside block
/endif
;@endfile
/load lib.tf
/libmacro x
/multi
/require lib.tf
/load missing.tf
/def keep -t'zzz' = /echo z
/save out.tf keep*
/save all.tf
/purge keep
/list keep
/load out.tf
/list keep
/quote /echo pre: 'data.txt
/quote -decho pre- #echo piped
/quote say #echo hello
/quote !ls
;@restrict file
/load lib.tf
/save x.tf
/quote /echo 'data.txt
/echo still ok
;@restrict world
/connect main
/addworld x host 1
/restrict shell
/restrict
