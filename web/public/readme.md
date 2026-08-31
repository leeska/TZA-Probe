# Embedded TZA Probe Web

Core embeds the TZA Probe administration frontend from:

<https://github.com/leeska/TZA-Web/tree/main>

The release workflows build that repository and copy these files before compiling Core:

```text
web/public/defaultTheme/
├── komari-theme.json
└── dist/
```

`dist/index.html` must exist when the Go embed package is compiled. The directory is intentionally ignored locally because it is generated from a specific frontend revision by the build workflow.

For a local integration build:

```bash
cd /path/to/TZA-Web
npm ci
npm run build

mkdir -p /path/to/TZA-Probe/web/public/defaultTheme/dist
cp -R dist/. /path/to/TZA-Probe/web/public/defaultTheme/dist/
cp komari-theme.json /path/to/TZA-Probe/web/public/defaultTheme/
```

The built-in frontend owns `/admin`, `/admin/monitoring` (with `/admin/probes` kept as a compatibility route), `/terminal`, and the installation/recovery routes. Optional display themes only own public presentation routes.
