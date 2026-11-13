/def -i -t'hello world' -p2 g1 = /echo g1 [%*] [%1] [%2] [%#]
/def -i -t'hello *' g2 = /echo g2 [%*] [%1] [%2] [%#]
/def -i -t'* tells you: *' g3 = /echo g3 [%*] [%1] [%2] [%3] [%4] [%#]
/def -i -t'a?c' g4 = /echo g4 [%*]
/def -i -t'*end' g5 = /echo g5 [%*]
/def -i -t'{foo|bar} baz' g6 = /echo g6 [%*]
/def -i -t'UPPER' g7 = /echo g7 [%*]
---
/trigger hello world

/trigger hello there big world

/trigger Bob tells you: hi there

/trigger abc

/trigger ac

/trigger the end

/trigger foo baz

/trigger bar baz

/trigger foox baz

/trigger upper

/trigger  hello   world

/trigger Hello World
