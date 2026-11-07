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

## SQLite and the pure-Go driver (message log)

The message log (`internal/store`) is a SQLite database, accessed through `modernc.org/sqlite` v1.61.1 (BSD-3-Clause), a pure-Go
translation of SQLite (public domain) that needs no C compiler. It brings these modules into the program: `modernc.org/libc`
(BSD-3-Clause; includes code derived from musl libc, MIT), `modernc.org/mathutil` and `modernc.org/memory` (BSD-3-Clause),
`golang.org/x/sys` (BSD-3-Clause), `github.com/google/uuid` (BSD-3-Clause), `github.com/remyoudompheng/bigfft` (BSD-3-Clause),
`github.com/mattn/go-isatty`, `github.com/dustin/go-humanize` and `github.com/ncruces/go-strftime` (MIT). All are compatible with
the GNU General Public License v3. The full licence texts and notices are in
[`licenses/modernc-sqlite-THIRD-PARTY.md`](licenses/modernc-sqlite-THIRD-PARTY.md), which is the notice file of the driver
itself (its Appendix A has every licence text; section 1 lists what is linked into a program).

    modernc.org/sqlite is licensed under the BSD-3-Clause licence:

    Copyright (c) 2017 The Sqlite Authors. All rights reserved.
    
    Redistribution and use in source and binary forms, with or without
    modification, are permitted provided that the following conditions are met:
    
    1. Redistributions of source code must retain the above copyright notice, this
    list of conditions and the following disclaimer.
    
    2. Redistributions in binary form must reproduce the above copyright notice,
    this list of conditions and the following disclaimer in the documentation
    and/or other materials provided with the distribution.
    
    3. Neither the name of the copyright holder nor the names of its contributors
    may be used to endorse or promote products derived from this software without
    specific prior written permission.
    
    THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
    ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
    WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
    DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
    FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
    DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
    SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
    CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
    OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
    OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
