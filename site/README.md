# MangaVeyra project website

`site/` is the public project page (screen **A1 – Landing – Desktop** in the design handoff, see
[docs/SCREENS.md](../docs/SCREENS.md)). It is plain HTML and CSS: no build step, no JavaScript, no external fonts,
scripts, trackers or analytics. A Content-Security-Policy `<meta>` tag limits the page to its own files.

```text
site/
  index.html            the page
  assets/site.css       tokens, components and layout (copied from web/src/styles, see the file header)
  assets/fonts/         woff2 files and OFL licences from @fontsource 5.3.0
  assets/icons/         Tabler Icons 3.48.0 webfont and its MIT licence
  assets/brand/         logo lockups and favicons from brand/logo
  .nojekyll             serve the folder as is on GitHub Pages
```

## Preview

Open `site/index.html` in a browser, or serve the folder:

```sh
python3 -m http.server --directory site 8000   # http://localhost:8000
```

## Deploy to GitHub Pages

Pages can publish only a branch root or `docs/`, so publish `site/` in one of two ways:

- **From a branch:** push the folder to a `gh-pages` branch with `git subtree push --prefix site origin gh-pages`,
  then choose *Settings → Pages → Deploy from a branch → gh-pages / (root)*.
- **With Actions:** a workflow that uploads `site/` with `actions/upload-pages-artifact` and deploys it with
  `actions/deploy-pages`, then choose *Settings → Pages → GitHub Actions*.

The page links to the repository on GitHub (install guide, docs, licence), so those links work once the repository
is public.

## Updating assets

The fonts, icon font and logos are copies, so the page works without `web/node_modules`. After a design-system
change, copy the files again from `web/node_modules/@fontsource/*/files/*-latin-*-normal.woff2`,
`web/node_modules/@tabler/icons-webfont/dist/fonts/tabler-icons.woff2` and `brand/logo/`, and keep the licence files
next to them. `site.css` declares only the icon glyphs the page uses; add a `.ti-<name>::before` rule (code point from
`tabler-icons.css`) for a new icon.

Copy on this page follows the approved artboard. Claims must match what is shipped: mark features that are not
built yet with the `Soon` tag and never describe roadmap features as available. Product rule 1 in
[AGENTS.md](../AGENTS.md) applies to this page like any other UI.
