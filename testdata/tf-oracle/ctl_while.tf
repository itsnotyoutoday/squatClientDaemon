/set i=0
---
/while (i < 3) /echo i=%i %; /set i=$[i+1] %; /done

/set i=0 %; /while (i < 10) /set i=$[i+1] %; /if (i == 3) /break %; /endif %; /done %; /echo broke at %i

/set i=0 %; /while (1 == 1) /set i=$[i+1] %; /if (i > 2) /break %; /endif %; /done %; /echo %i

/set i=0 %; /while (i < 2) /set j=0 %; /while (j < 2) /echo %i%j %; /set j=$[j+1] %; /done %; /set i=$[i+1] %; /done

/set i=5 %; /while (i < 3) /echo never %; /done %; /echo skipped

