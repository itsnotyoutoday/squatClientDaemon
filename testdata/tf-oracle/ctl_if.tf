/set x=3
---
/if (x == 3) /echo yes %; /else /echo no %; /endif

/if (x == 4) /echo yes %; /else /echo no %; /endif

/if (x == 1) /echo one %; /elseif (x == 3) /echo three %; /else /echo other %; /endif

/if (x == 1) /echo one %; /elseif (x == 2) /echo two %; /endif %; /echo after

/if (x > 1) /echo a %; /if (x > 2) /echo b %; /else /echo c %; /endif %; /endif

/if (x) /echo truthy %; /endif

/if (!x) /echo falsy %; /else /echo nonzero %; /endif

/if ("") /echo empty-true %; /else /echo empty-false %; /endif

/if (x == 3) /echo inline %; /endif

/if /test x == 3%; /then /echo old-form %; /endif

/if (x == 3) /echo a %; /echo b %; /endif %; /echo c

/if (x==3) /echo nospace %; /endif
