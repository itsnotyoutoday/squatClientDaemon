#!/usr/bin/env python3 -I
"""A local TCP server that accepts connections and stays silent. Writes its
port to the file given as argv[1]; sends each line of argv[2] on connect. Used by run-tf.sh for ;@net cases."""
import socket, sys, time
s = socket.socket()
s.bind(("127.0.0.1", 0))
s.listen(5)
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
conns = []
s.settimeout(1)
end = time.time() + 30
while time.time() < end:
    try:
        c, _ = s.accept()
        conns.append(c)
        if len(sys.argv) > 2:
            for line in open(sys.argv[2]).read().splitlines():
                c.sendall(line.encode() + b"\r\n")
    except socket.timeout:
        pass
