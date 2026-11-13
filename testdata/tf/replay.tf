/def -t'*attack*' -g -ahCred -E'1' atk = /send flee %; /notify under attack
/def -t'*gold*' -n1 -P0BCyellow gld = /set seen=1 %; /echo got gold
/def -t'*hp*' -mglob hp = /echo hp line
/set live=0
;@replay main you attack the orc
;@replay main some gold
;@var seen
;@line main you attack the orc
;@line main some gold
;@var seen
;@replay main more gold
;@line main more gold
