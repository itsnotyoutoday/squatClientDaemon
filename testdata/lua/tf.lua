-- Hand a line to the TF engine.
squat.trigger{match = "* arrives.", fn = function(m)
  squat.tf("/echo -aB Welcome, " .. m[1] .. "!")
end}
