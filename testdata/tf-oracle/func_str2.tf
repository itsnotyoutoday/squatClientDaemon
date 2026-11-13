/echo r [$[substr("hello", 1, 3)]]

/echo r [$[replace("o", "0", "hello world")]]

/echo r [$[replace("", "x", "hello")]]

/echo r [$[strrep("-", 5)]]

/echo r [$[strcat("a", "")]] [$[strcat("", "")]]

/echo r $[strlen(strrep("ab", 50))]

