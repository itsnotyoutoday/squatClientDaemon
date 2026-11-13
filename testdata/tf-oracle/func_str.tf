/echo r $[strlen("")] $[strlen("hello")] $[strlen("a b c")] $[strlen(12345)]

/echo r $[strcat("a", "b", "c")] $[strcat("x")] $[strcat(1, 2)]

/echo r [$[substr("hello", 1, 3)]] [$[substr("hello", 0)]] [$[substr("hello", 2)]] [$[substr("hello", 4, 10)]] [$[substr("hello", 5)]] [$[substr("hello", 9)]]

/echo r [$[substr("hello", -2)]] [$[substr("hello", -2, 1)]] [$[substr("hello", 1, -1)]] [$[substr("hello", 1, -2)]] [$[substr("hello", -9)]]

/echo r [$[substr("hello", 0, 0)]] [$[substr("hello", 1, 1)]] [$[substr("hello", -1)]]

/echo r $[strstr("hello", "l")] $[strstr("hello", "lo")] $[strstr("hello", "z")] $[strstr("hello", "")] $[strstr("", "a")] $[strstr("hello", "H")]

/echo r $[strchr("hello", "l", 3)] $[strchr("hello", "l", 4)] $[strchr("hello", "l", -2)] $[strchr("hello", "h", -1)]

/echo r $[strchr("hello", "l")] $[strchr("hello", "xyz")] $[strchr("hello", "ol")] $[strchr("hello", "")]

/echo r $[strrchr("hello", "l", 2)] $[strrchr("hello", "l", 1)] $[strrchr("hello", "l", -3)]

/echo r $[strstr("hello", "l", 3)] $[strstr("hello", "l", 4)] $[strstr("hello", "l", -2)] $[strstr("hello", "h", 9)]

/echo r [$[tolower("ABCDE", 2)]] [$[toupper("abcde", 2)]] [$[toupper("abcde", 0)]] [$[toupper("abc", 9)]]

/echo r $[strrchr("hello", "l")] $[strrchr("hello", "h")] $[strrchr("hello", "z")] $[strrchr("hello", "lo")]

/echo r [$[strrep("ab", 3)]] [$[strrep("ab", 0)]] [$[strrep("", 5)]] [$[strrep("a", 1)]]

/echo r [$[tolower("HeLLo 1")]] [$[toupper("HeLLo 1")]] [$[tolower("")]]

/echo r [$[pad("ab", 5)]] [$[pad("ab", -5)]] [$[pad("abcdef", 3)]] [$[pad("abcdef", -3)]] [$[pad("ab", 0)]]

/echo r [$[pad("ab", 5, "*")]] [$[pad("ab", -5, "*")]] [$[pad(12, 5)]] [$[pad("a", 3, "xy")]]

/echo r [$[pad("a", 3, "b", 4, "c")]] [$[pad("a", 2, "b", -3)]]

/echo r $[strcmp("a", "b") < 0] $[strcmp("b", "a") > 0] $[strcmp("a", "a")] $[strcmp("a", "A") > 0] $[strcmp("abc", "abd") < 0] $[strcmp("", "a") < 0] $[strcmp("10", "9") < 0]

/echo r $[strncmp("abcd", "abxx", 2)] $[strncmp("abcd", "abxx", 3) < 0] $[strncmp("a", "b", 0)] $[strncmp("abc", "ABC", 3) > 0]

/echo r $[ascii("A")] $[ascii("ab")] $[ascii("")] $[char(65)] $[char(97)] $[ascii(char(200))]

/echo r [$[replace("l", "L", "hello")]] [$[replace(" ", "-", "a b")]] [$[replace("ll", "", "hello")]] [$[replace("x", "y", "")]]

