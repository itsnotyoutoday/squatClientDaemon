-- Gag channel spam, but still count it.
squat.trigger{
  match = "^<Spam> ",
  regex = true,
  gag = true,
  fn = function(m)
    squat.var.set("spam_count", squat.var.get("spam_count", 0) + 1)
  end,
}
