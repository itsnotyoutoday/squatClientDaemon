-- Auto-heal: cast a heal spell when HP drops below a threshold.
local threshold = 0.4

squat.trigger{
  match = "^HP: (\\d+)/(\\d+)",
  regex = true,
  fn = function(m)
    local hp, max = tonumber(m[1]), tonumber(m[2])
    squat.var.set("hp", hp)
    if hp / max < threshold and squat.now() - (squat.var.get("last_heal", 0)) > 3 then
      squat.var.set("last_heal", squat.now())
      squat.send("cast heal")
    end
  end,
}
