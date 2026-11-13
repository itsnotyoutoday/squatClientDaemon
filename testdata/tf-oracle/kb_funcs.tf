/echo r [$[kblen()]] [$[kbpoint()]] [$[kbhead()]] [$[kbtail()]]

/input hello world (x)

/echo r [$[kblen()]] [$[kbpoint()]] [$[kbhead()]] [$[kbtail()]]

/echo r [$[kbgoto(5)]] [$[kbpoint()]] [$[kbhead()]] [$[kbtail()]]

/echo r [$[kbgoto(99)]] [$[kbgoto(-3)]] [$[kbgoto(0)]]

/echo r [$[kbwordright()]] [$[kbwordright(6)]] [$[kbwordleft(11)]] [$[kbwordleft()]]

/echo r [$[kbmatch(12)]] [$[kbmatch(14)]] [$[kbmatch(0)]] [$[kbmatch()]] [$[kbmatch(1)]]

/echo r [$[kbgoto(5)]] [$[kbdel(8)]] [$[kbhead()]] [$[kbtail()]]

/echo r [$[kbdel(0)]] [$[kbhead()]] [$[kbtail()]]

/input abc

/dokey left

/echo r [$[kbpoint()]]

/dokey home

/echo r [$[kbpoint()]]

/dokey end

/echo r [$[kbpoint()]]

/dokey wleft

/echo r [$[kbpoint()]] [$[kbhead()]]

/dokey dline

/echo r [$[kblen()]] [$[kbpoint()]]
