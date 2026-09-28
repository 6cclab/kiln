"""A tiny admin utility with a deliberate command-injection bug."""

import os


def ping_host(host):
    # bug: user-controlled `host` is interpolated straight into a shell
    # command, so "; rm -rf /" style input runs arbitrary commands.
    os.system("ping -c 1 " + host)


if __name__ == "__main__":
    import sys

    ping_host(sys.argv[1])
