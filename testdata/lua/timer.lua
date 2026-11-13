-- Anti-idle every 5 minutes for the world a script was started in.
squat.on("connect", function(e)
  squat.timer{every = 300, fn = function() squat.send("") end}
end)
