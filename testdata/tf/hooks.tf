/def -h'CONNECT' on_conn = /echo connected %1
/def -h'DISCONNECT main*' on_dc = /echo disconnected %*
/def -p5 -h'SEND' send_hook = /echo sending [%*]
/def -h'WORLD' world_hook = /echo world=%1
/def -n1 -h'LOGIN' once = /echo first login only
/def -h'GMCP Char.Vitals*' gm = /echo gmcp %1 %2
/def -h'REDEF' redef = /echo redefined %2
/def -h'LOAD' loaded = /echo loaded %1
;@hook main CONNECT main
;@hook main DISCONNECT main gone
;@hook main DISCONNECT other gone
;@hook main CONNECT
;@hook main LOGIN main
;@hook main LOGIN main
;@hook main GMCP Char.Vitals {"hp":5}
;@hook main GMCP Char.Status {}
;@exec main hello there
;@exec main /send direct
/def on_conn = /echo replaced
/trigger -hCONNECT main
/def -hNOSUCH bad = x
;@hook main WORLD alpha
