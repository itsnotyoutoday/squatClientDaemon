/def -i ud1 = /echo one
/def -i ud2 = /echo two
/def -i ud3 = /echo three
/def -i udx1 = /echo x1
/def -i udx2 = /echo x2
/def -i -t'purge me' pm1 = /echo pm1
---
/ud1

/undef ud1

/ud1

/undef ud2 ud3

/ud2

/undef nosuch

/udx1

/purge udx*

/udx1

/udx2

/trigger purge me

/purge pm1

/trigger purge me

/def -i dd = /echo first

/def -i dd = /echo second

/dd
