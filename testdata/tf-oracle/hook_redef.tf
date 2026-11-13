/def -i -hREDEF redefhook = /echo REDEF [%*] [%1] [%2]
/def -i -p2 -h'REDEF macro q*' redefq = /echo REDEFQ [%2]
---
/def -i rdf = /echo one

/def -i rdf = /echo two

/def -i rdf = /echo two

/def -i qq = /echo one

/def -i qq = /echo two

/rdf

/undef rdf

/undef rdf
