# Third-party notices

navtexd is distributed under the GNU General Public License v3. It includes the following third-party material.

## navtex-rs (soft repetition lock)

`internal/softlock` is a Go port of the weak-signal lock in navtex-rs (`navtex-core/src/{softlock,score,combine,charset}.rs`),
https://github.com/bgm60/navtex-rs, by bgm60, licensed GPL-3.0-or-later (compatible with this program's GPL v3).
Its test vectors (`internal/softlock/testdata`, reduced to the soft bit streams, lock traces and decoded text) come from the same
project, which in turn captured them from the Python reference decoder bgm60/navtex-decode. The algorithm and its parameters are
theirs; see `docs/soft-lock-design.md` for what we changed or still have to calibrate.

## Tabler Icons

Some of the icons in the web interface (filter, filter-edit, sort-ascending-numbers, sort-descending-numbers, arrow-bar-to-right,
arrow-back-up, eye, report-medical, trash, database, minus-vertical, filter-x, thumb-up, cancel, playlist-x, file-x, edit, device-floppy, clipboard-copy, restore, wave-sine) are from Tabler Icons 3.49.0, https://tabler.io/icons,
used under the MIT licence:

    MIT License
    
    Copyright (c) 2020-2026 Paweł Kuna
    
    Permission is hereby granted, free of charge, to any person obtaining a copy
    of this software and associated documentation files (the "Software"), to deal
    in the Software without restriction, including without limitation the rights
    to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
    copies of the Software, and to permit persons to whom the Software is
    furnished to do so, subject to the following conditions:
    
    The above copyright notice and this permission notice shall be included in all
    copies or substantial portions of the Software.
    
    THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
    IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
    FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
    AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
    LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
    OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
    SOFTWARE.
