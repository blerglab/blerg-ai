# Third-party notices

Blerg is licensed under the Apache License 2.0 (see [`LICENSE`](LICENSE)). Parts of it are adapted
from the third-party works listed here, under their own licenses. Dependencies installed by a package
manager carry their own license files and are not listed.

## github-markdown-css

`packages/chat/src/components/markdown.css` adapts the styling of
[github-markdown-css](https://github.com/sindresorhus/github-markdown-css) (its values for type,
spacing, code, tables, quotes and rules), mapped onto Blerg's theme tokens.

The runner's and the Board's web bundles and the package's JavaScript carry a short credit as a
license comment; the full notice is below.

```
MIT License

Copyright (c) Sindre Sorhus <sindresorhus@gmail.com> (https://sindresorhus.com)

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and
associated documentation files (the "Software"), to deal in the Software without restriction,
including without limitation the rights to use, copy, modify, merge, publish, distribute,
sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial
portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT
LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN
NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

## Seti UI file icons

`packages/chat/src/components/fileIconPaths.ts` bundles 47 monochrome glyphs from
[Seti UI](https://github.com/jesseweed/seti-ui), converted to SVG paths from the
Seti WOFF distributed by VS Code. The seven folder variants use Blerg's original
folder outlines and badges (the GitHub and npm badges reuse the Seti glyphs).
Colors are adapted for contrast with the host's light or dark foreground.
No font or remote asset is loaded at runtime.

License verified offline against the upstream VS Code repository files shipped in
VS Code commit `df53daabb18cd157bdb08c7f01c34df936cf12f4`:

- [`extensions/theme-seti/ThirdPartyNotices.txt`](https://github.com/microsoft/vscode/blob/df53daabb18cd157bdb08c7f01c34df936cf12f4/extensions/theme-seti/ThirdPartyNotices.txt)
  explicitly attributes the Seti UI project and contains the full MIT grant below.
- [`extensions/theme-seti/package.json`](https://github.com/microsoft/vscode/blob/df53daabb18cd157bdb08c7f01c34df936cf12f4/extensions/theme-seti/package.json)
  declares MIT; its README and `icons/vs-seti-icon-theme.json` identify
  `jesseweed/seti-ui` as the glyph, color, and association source.

Source SHA-256 hashes (exact installed bytes, including original line endings):

```
be25a8e2c617f7214c99a256305fb6b2d976e3a698c44bda424f617079dcff9f ThirdPartyNotices.txt
377ea60543ffdb173ff3fa528250f5c6d7ac80d295090794a4d07ac78341db8d README.md
4b2a385edf0c4adab91d44a35074519b7dc81cc5ae9ff4a43a52b279107792b0 package.json
b127762058f89b37b08d76185e3b0558f5c28859e39a3840a27cb28b9739d6e2 icons/seti.woff
bbc6de399ac76b43a986237ae86dc714d0fee2df906924cba495d442b9a0066c icons/vs-seti-icon-theme.json
```

Regenerate using `packages/chat/scripts/extract-seti.py` with that verified
`theme-seti` directory (Python fontTools required only for regeneration).
The complete copyright and permission notice also travels in the compiled JavaScript.

```
MIT License

Copyright (c) 2014 Jesse Weed

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```
