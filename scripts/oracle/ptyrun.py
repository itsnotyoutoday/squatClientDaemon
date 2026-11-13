#!/usr/bin/env python3 -I
"""Run argv under a pty (tf refuses to print without a terminal) and discard
the screen output. usage: ptyrun.py HOME TFLIBDIR TIMEOUT cmd args..."""
import os, pty, select, sys, time

home, lib, timeout = sys.argv[1], sys.argv[2], float(sys.argv[3])
argv = sys.argv[4:]
env = dict(os.environ, TERM="dumb", HOME=home, TFLIBDIR=lib, LANG="en_US.UTF-8")
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(argv[0], argv, env)
end = time.time() + timeout
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.1)
    if r:
        try:
            if not os.read(fd, 65536):
                break
        except OSError:
            break
    else:
        try:
            p, _ = os.waitpid(pid, os.WNOHANG)
            if p:
                sys.exit(0)
        except ChildProcessError:
            sys.exit(0)
try:
    os.kill(pid, 9)
except OSError:
    pass
sys.exit(0)
