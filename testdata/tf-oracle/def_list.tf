/def dl1 = /echo one
/def -t'foo *' dl2 = /echo two
/def -mregexp -t'^x+$' -p5 dl3 = /echo three
/def -n3 -t'shot' dl4 = /echo four
/def -F -t'fall' -p2 dl5 = /echo five
/def -E(1) -t'cond' dl7 = /echo seven
/def -hCONNECT dl8 = /echo eight
/def -c50 -t'chance' dl9 = /echo nine
/def -ag -t'gagme' dl10 = /echo ten
/def -ah -t'hilite' dl11 = /echo eleven
/def -aCred -t'colour' dl12 = /echo twelve
/def -1 -t'once' dl13 = /echo thirteen
/def -b'^Xz' dl14 = /echo fourteen
---
/list dl1

/list dl2

/list dl3

/list dl4

/list dl5


/list dl7

/list dl8

/list dl9

/list dl10

/list dl11

/list dl12

/list dl13

/list dl14

/list dl*

/list nosuchmacro
