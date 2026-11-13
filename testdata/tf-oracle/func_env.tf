/echo r [$[columns()]] [$[lines()]] [$[winlines()]] [$[isatty()]] [$[systype()]]

/echo r [$[nactive()]] [$[nmail()]] [$[nread()]] [$[morepaused()]] [$[moresize()]]

/echo r [$[strcmpattr("a","a")]] [$[encode_attr("a")]] [$[strip_attr("a")]] [$[decode_attr("a")]]

/echo r [$[textencode("a b")]] [$[textdecode(textencode("a b"))]] [$[textencode("x")]] [$[textencode("a_b")]]

/echo r [$[getpid() > 0]] [$[idle() >= 0]] [$[sidle() >= 0]] [$[cputime() >= 0]]

/echo r [$[fg_world()]] [$[world_info()]] [$[is_connected()]] [$[is_open()]]

/echo r [$[filename("/x")]]

/echo r [$[time() > 1000000000]] [$[mktime(1970, 1, 1, 0, 0, 0) == mktime(1970)]]
