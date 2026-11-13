-- Truecolor highlight of the whole line, and of just the matched word.
squat.trigger{match = "*tells you*", fg = "#ff8800", b = true}
squat.trigger{match = "treasure", regex = true, hilite = "match", fg = "#ffd700", bg = 52}
