/echo -aCred red text
/echo -aC196,u indexed 256
/echo -aC#ff8800 truecolor
/echo -aBC#001122,C#aabbcc,h,i both
/echo -aCbrightgreen,BCgray5,d bright+gray
/echo -aCrgb530 cube
/echo -p plain @{Cred}red@{n} @{BCblue,u}blue-u@{n} @{C#102030}tc@{n} lit@@at
/echo -aCnosuchcolor bad
/echo -ah hilite-only
/echo $[decode_attr("@{C#ff0000}x@{n}y")]
/echo $[decode_attr("@{C196,BC21}a@{Cred}b")]
/echo $[strip_attr(decode_attr("@{Cred}hello@{n}"))]
/def -t'x*' -aC#abcdef,B hl
;@line main xyz
/list
/def -P1Cred -P2C#00ff00 -mregexp -t'(a)(b)' pp
/list pp
