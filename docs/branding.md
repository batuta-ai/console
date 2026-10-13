# Console presentation assets

[Documentation index](README.md) · [English](../README.md) · [Português](../README.pt-BR.md)

The approved symbol originates in `batuta-ai/.github`, the official organization
profile and brand-kit repository. The supplied official repository READMEs and
reference PNGs establish the palette, repository-label typography, bilingual
presentation and fixed dimensions. These Console assets are local derivatives;
this work does not change the shared organization repository.

The local [approved symbol](../assets/batuta-symbol.svg) has SHA-256
`19bd23fef940e7d20ddafb29a3502f47108bd5fbfc809357a22474ff8e4dc4ef`.
Each presentation SVG embeds its complete path and original
`viewBox="136.5 33.5 660 660"`. Only the square viewport size and placement
change. Scale proportionally; never crop, stretch, rotate or mirror. The
`batuta-ai / console` text is a repository label, following the reference
monospace label style, not a newly typeset Batuta wordmark. Do not substitute
font text for the approved logo geometry.

| Use | Tracked source | Rendered image | Dimensions |
| --- | --- | --- | --- |
| Dark README header | [SVG](../assets/readme-header-console-dark.svg) | [PNG](../assets/readme-header-console-dark.png) | 1600 × 240 |
| Light README header | [SVG](../assets/readme-header-console-light.svg) | [PNG](../assets/readme-header-console-light.png) | 1600 × 240 |
| Social preview | [SVG](../assets/social-console.svg) | [PNG](../assets/social-console.png) | 1280 × 640 |

Both repository READMEs use a local `<picture>` with the dark image selected
by `prefers-color-scheme: dark` and the light image as fallback. These headers
and language navigation links are intended for repository checkout and repository
view: current distribution archives omit these images and `README.pt-BR.md`.
They are not currently packaged, and packaging or distribution inclusion is
not yet assigned or implemented. The social preview is a local asset ready for repository
settings; it is not automatically published by the README. Descriptions appear
in Portuguese and English; images contain no product screenshot or operational
qualification claim.

The reference palette uses symbol/accent teal `#009F91`, dark-label teal
`#2FC4B6`, dark background `#0D1117` and light background `#F7F8F6`.
System monospace and sans-serif fonts render the repository labels and
supporting descriptions. Font availability may affect text metrics; retain
readability and inspect the outputs after regeneration.

## Regeneration

From the repository root, with the existing `rsvg-convert` and ImageMagick
`magick` executables installed, render directly from the tracked SVGs:

```sh
rsvg-convert -o assets/readme-header-console-dark.png assets/readme-header-console-dark.svg
rsvg-convert -o assets/readme-header-console-light.png assets/readme-header-console-light.svg
rsvg-convert -o assets/social-console.png assets/social-console.svg
magick identify -format '%f: %wx%h\n' assets/readme-header-console-dark.png assets/readme-header-console-light.png assets/social-console.png
shasum -a 256 assets/batuta-symbol.svg
```

Expected dimensions are 1600x240, 1600x240 and 1280x640 respectively, and the
symbol hash must match the value above. Inspect all three renders for full
symbol visibility, legible descriptions and uncropped labels. Regeneration
uses no network assets or new project dependencies.
