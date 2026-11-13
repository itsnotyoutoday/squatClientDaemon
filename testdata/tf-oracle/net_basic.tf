;@net
;@screen
;@send you hit the orc
;@send secret stuff
;@send no match here
;@send plain text
;@send fall x
;@send multi a
;@send multi b
;@send  leading spaces
/def -i -t'you hit *' hit = /echo hit [%1] [%2]
/def -i -t'secret *' -ag gagged = /echo gagged-fired
/def -i -t'plain *' -p2 plain1 = /echo plain1
/def -i -t'plain *' -p1 plain0 = /echo plain0
/def -i -t'fall *' -F -p3 fall1 = /echo fall1
/def -i -t'fall *' -p2 fall2 = /echo fall2
/def -i -t'multi *' -n1 shot1 = /echo shot1
---
/echo [$[fake_recv("fake line")]]

/test fake_recv("you hit the orc")

/test fake_recv("plain text")
