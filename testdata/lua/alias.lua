-- k <target>  ->  kill <target>
squat.alias{match = "k *", fn = function(m) squat.send("kill " .. m[1]) end}
