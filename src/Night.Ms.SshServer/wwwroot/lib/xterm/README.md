# xterm.js (vendored)

Vendored to keep the in-browser terminal page self-contained — no third-party CDN requests on every `/terminal` visit.

| File              | Source                                                  | Version |
|-------------------|---------------------------------------------------------|---------|
| `xterm.min.js`    | `https://unpkg.com/@xterm/xterm@5.5.0/lib/xterm.js`     | 5.5.0   |
| `xterm.min.css`   | `https://unpkg.com/@xterm/xterm@5.5.0/css/xterm.css`    | 5.5.0   |
| `addon-fit.min.js`| `https://unpkg.com/@xterm/addon-fit@0.10.0/lib/addon-fit.js` | 0.10.0 |

License: MIT (xterm.js project).

## Upgrading

```sh
curl -fsSL -o xterm.min.js  https://unpkg.com/@xterm/xterm@<VER>/lib/xterm.js
curl -fsSL -o xterm.min.css https://unpkg.com/@xterm/xterm@<VER>/css/xterm.css
curl -fsSL -o addon-fit.min.js https://unpkg.com/@xterm/addon-fit@<VER>/lib/addon-fit.js
```

Update the version column above in the same commit. Visually verify `/terminal` still renders and resizes correctly before merging.
