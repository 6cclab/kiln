#!/usr/bin/env python3
"""Write a PNG of random noise: mkpng.py <out> <width> <height>.

Noise does not compress, so the file is about width*height*3 bytes. That
makes it a reliable "too large to attach as-is" image for the image
downscale scenario without shipping a binary fixture."""
import os
import struct
import sys
import zlib

out, w, h = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])


def chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)


rows = b"".join(b"\x00" + os.urandom(w * 3) for _ in range(h))
with open(out, "wb") as f:
    f.write(b"\x89PNG\r\n\x1a\n")
    f.write(chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)))
    f.write(chunk(b"IDAT", zlib.compress(rows, 1)))
    f.write(chunk(b"IEND", b""))
