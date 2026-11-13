/echo r $[strlen("abc") + strlen("de")] $[strlen(strcat("ab", "cd"))] $[toupper(substr("hello", 1, 3))]

/echo r $[abs(-3) * 2] $[trunc(3.9)] $[trunc(-3.9)] $[mod(10, 3)] $[mod(-10, 3)] $[mod(10, -3)]

/echo r $[pow(2, 10)] $[sqrt(16)] $[abs(-2.5)] $[abs(-4)] $[abs(3)]

/echo r $[sqrt(2)] $[pow(2, 0.5)] $[pow(2, -1)] $[pow(0, 0)] $[pow(3, 3)] $[pow(-2, 3)]

/echo r $[trunc(5)] $[trunc(0.99)] $[trunc("7.5")]

/echo r $[sin(0)] $[cos(0)] $[exp(0)] $[ln(1)] $[log10(1000)] $[atan(0)]

