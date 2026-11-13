/addworld -Ttiny -x main example.org 4201
/addworld -Ttiny alt bob secret example.com 4202
/listworlds
/connect main
/world alt
/fg main
/dc alt
/unworld alt
/saveworld
/echo $[world_info("main", "host")] $[world_info("main", "port")] $[world_info("main", "tls")]
/log -wmain out.log
/log off
/limit foo*
/unlimit
/relimit
/input hello
/dokey recall-prev
/localecho on
/status_add -A foo
/status_rm foo
/recall -wmain 10
/beep
/notify Title | body text
/bind ^A = /echo ctrl-a pressed
/bind F1 /echo f1
;@key main ^A
;@key main F1
;@key main F2
/unbind ^A
;@key main ^A
/trigger test line
/def -t'test line' tl = /echo triggered
/trigger test line
/trigpc 100 5 test line
/trigpc 100 -5 test line
/help echo
/version
/exit
