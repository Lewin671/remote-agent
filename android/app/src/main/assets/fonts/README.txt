Sarasa Fixed SC Regular v1.0.42, symbols subset
https://github.com/be5invis/Sarasa-Gothic/releases/tag/v1.0.42
Modified: a subset of SarasaFixedSC-Regular.ttf (original SHA256
eca2c4618d27c83059e80d34000c41a48e14e578904a733f318732cb6924c2f6) holding only the symbol
ranges, made with fontTools:
  pyftsubset SarasaFixedSC-Regular.ttf --unicodes='U+2000-2BFF,U+E000-F8FF,U+1FB00-1FBFF' \
    --layout-features='' --no-hinting --name-IDs='*' --notdef-outline
Licensed under SIL OFL 1.1 (OFL.txt).
The terminal uses it for symbols JetBrains Mono lacks; no font or terminal content is sent to the server.
SHA256: 26276c66f84efc0eeeb39a68c8bd39cc9335d353543ba045fd56b271d6e2f20b
