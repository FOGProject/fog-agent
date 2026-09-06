#!/usr/bin/env python3
"""Write the two bitmaps the installer's dialogs use.

wixl's ui extension ships WiX's artwork re-encoded as BI_RLE8, and Windows
Installer cannot draw a compressed BMP: every Bitmap control in the package
rendered as the red "broken image" square, on the stock Welcome and Exit
pages as well as ours. The icons were fine, which is what pointed at the
compression rather than at the Binary table.

Rather than ship a decompressed copy of somebody else's artwork, these are
plain and ours. Both are 24-bit uncompressed, in the sizes WixUI's dialogs
are laid out for, and both keep white where the dialogs put black text over
the top:

  banner  493x58   white, with an accent block on the right past the text
  dialog  493x312  a blue panel down the left third, white where the
                   heading and description sit (from x=135 dialog units)

build/msi.sh feeds them to msibuild, which overwrites the streams wixl put
in the Binary table.
"""
import struct
import sys

BLUE_TOP = (0x1F, 0x3A, 0x5F)
BLUE_BOTTOM = (0x3D, 0x6B, 0xA0)
ACCENT = (0x2E, 0x54, 0x83)
WHITE = (0xFF, 0xFF, 0xFF)


def write_bmp(path, width, height, pixel):
    """pixel(x, y) -> (r, g, b), with y counted from the top."""
    stride = (width * 3 + 3) // 4 * 4
    rows = []
    for y in range(height - 1, -1, -1):  # BMP rows run bottom to top
        row = bytearray()
        for x in range(width):
            r, g, b = pixel(x, y)
            row += bytes((b, g, r))
        row += b"\0" * (stride - len(row))
        rows.append(bytes(row))
    bits = b"".join(rows)
    header = struct.pack(
        "<2sIHHI", b"BM", 14 + 40 + len(bits), 0, 0, 14 + 40
    ) + struct.pack(
        "<IiiHHIIiiII", 40, width, height, 1, 24, 0, len(bits), 2835, 2835, 0, 0
    )
    with open(path, "wb") as fh:
        fh.write(header + bits)


def mix(a, b, t):
    return tuple(round(a[i] + (b[i] - a[i]) * t) for i in range(3))


def banner(x, y):
    # White under the title and description, an accent block to the right of
    # where the text ends (355 dialog units of 370, so past 473 of 493 px --
    # keep the block clear of it at 430).
    if x >= 430:
        return ACCENT
    if x >= 424:
        return mix(WHITE, ACCENT, (x - 424) / 6)
    return WHITE


def dialog(x, y):
    # The heading and description controls start at dialog unit 135 of 370,
    # which is pixel 180 of 493. The panel stops short of it.
    panel = 168
    if x < panel:
        return mix(BLUE_TOP, BLUE_BOTTOM, y / 311)
    if x < panel + 6:
        return mix(mix(BLUE_TOP, BLUE_BOTTOM, y / 311), WHITE, (x - panel) / 6)
    return WHITE


if __name__ == "__main__":
    out = sys.argv[1].rstrip("/")
    write_bmp(out + "/banner.bmp", 493, 58, banner)
    write_bmp(out + "/dialog.bmp", 493, 312, dialog)
