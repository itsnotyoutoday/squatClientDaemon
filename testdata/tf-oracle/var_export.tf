/set ev=1
/export ev
---
/echo [%ev]

/setenv se=val

/echo [%se]

/unset ev

/echo [%{ev-gone}]

/set v1=a %; /set v2=b  %; /listvar v1%; /listvar v2

