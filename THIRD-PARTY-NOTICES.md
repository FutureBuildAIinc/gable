# Third-Party Notices

Gable depends on third-party open-source software. This file summarizes those
dependencies and their licenses. It is provided for convenience and attribution;
each dependency's own license text (in its source repository or module) is the
authoritative statement of its terms.

Gable's own source is licensed **per component** under the OpenLBM Standard —
see [`LICENSE-MAP.md`](./LICENSE-MAP.md) and [`LICENSES/`](./LICENSES/). The
notices below cover **external** dependencies only.

The lists were generated from `backend/go.mod` / `go list -m all` and
`app/package.json`. Versions drift over time — regenerate when upgrading:

```bash
cd backend && go list -m all           # Go modules
cat app/package.json                   # npm packages
```

---

## Apache-2.0 NOTICE-preservation requirement

Several dependencies below are licensed under the **Apache License, Version
2.0**. Section 4(d) of that license requires that, when you redistribute the
work (including in binary/compiled form), you **retain the attribution notices**
those projects provide in their `NOTICE` files. Accordingly, when you
redistribute Gable:

- Keep this `THIRD-PARTY-NOTICES.md` and the top-level [`NOTICE`](./NOTICE) file
  alongside the distribution.
- Preserve the `LICENSE` and `NOTICE` files shipped by the Apache-2.0
  dependencies (marked **Apache-2.0** in the tables below).

Apache-2.0 dependencies in this project include (Go) `MicahParks/keyfunc`,
`MicahParks/jwkset`, `go-jose/go-jose`, `prometheus/client_golang`,
`prometheus/client_model`, `prometheus/common`, `prometheus/procfs`,
`pdfcpu/pdfcpu`, `richardlehane/mscfb`, `richardlehane/msoleps`,
`go.yaml.in/yaml`, `gopkg.in/yaml.v2`; and (npm) `html5-qrcode`, and (dev tooling) `typescript`.

---

## Backend — Go modules

### Direct dependencies

| Module | License |
|---|---|
| github.com/MicahParks/keyfunc/v3 | Apache-2.0 |
| github.com/go-jose/go-jose/v4 | Apache-2.0 |
| github.com/golang-jwt/jwt/v5 | MIT |
| github.com/google/uuid | BSD-3-Clause |
| github.com/jackc/pgx/v5 | MIT |
| github.com/johnfercher/maroto/v2 | MIT |
| github.com/joho/godotenv | MIT |
| github.com/prometheus/client_golang | Apache-2.0 |
| github.com/robfig/cron/v3 | MIT |
| github.com/xuri/excelize/v2 | BSD-3-Clause |
| golang.org/x/crypto | BSD-3-Clause |

### Notable indirect dependencies

| Module | License |
|---|---|
| github.com/MicahParks/jwkset | Apache-2.0 |
| github.com/beorn7/perks | MIT |
| github.com/boombuler/barcode | MIT |
| github.com/cespare/xxhash/v2 | MIT |
| github.com/f-amaral/go-async | MIT |
| github.com/hhrutter/lzw | BSD-3-Clause |
| github.com/hhrutter/tiff | BSD-3-Clause |
| github.com/jackc/pgpassfile | MIT |
| github.com/jackc/pgservicefile | MIT |
| github.com/jackc/puddle/v2 | MIT |
| github.com/johnfercher/go-tree | MIT |
| github.com/mattn/go-runewidth | MIT |
| github.com/munnerz/goautoneg | BSD-3-Clause |
| github.com/pdfcpu/pdfcpu | Apache-2.0 |
| github.com/phpdave11/gofpdf | MIT |
| github.com/pkg/errors | BSD-2-Clause |
| github.com/prometheus/client_model | Apache-2.0 |
| github.com/prometheus/common | Apache-2.0 |
| github.com/prometheus/procfs | Apache-2.0 |
| github.com/richardlehane/mscfb | Apache-2.0 |
| github.com/richardlehane/msoleps | Apache-2.0 |
| github.com/rivo/uniseg | MIT |
| github.com/tiendc/go-deepcopy | MIT |
| github.com/xuri/efp | BSD-3-Clause |
| github.com/xuri/nfp | BSD-3-Clause |
| go.yaml.in/yaml/v2 | Apache-2.0 |
| golang.org/x/image | BSD-3-Clause |
| golang.org/x/net | BSD-3-Clause |
| golang.org/x/sync | BSD-3-Clause |
| golang.org/x/sys | BSD-3-Clause |
| golang.org/x/text | BSD-3-Clause |
| golang.org/x/time | BSD-3-Clause |
| google.golang.org/protobuf | BSD-3-Clause |
| gopkg.in/yaml.v2 | Apache-2.0 |

---

## Frontend — npm packages

### Runtime dependencies (shipped in the browser bundle)

| Package | License |
|---|---|
| chart.js | MIT |
| clsx | MIT |
| date-fns | MIT |
| html5-qrcode | Apache-2.0 |
| leaflet | BSD-2-Clause |
| lit | BSD-3-Clause |
| lucide | ISC |
| tailwind-merge | MIT |

### Build / dev tooling (not shipped to end users)

These run only at build and test time and are not distributed in the client
bundle, but are listed for completeness.

| Package | License |
|---|---|
| typescript | Apache-2.0 |
| vite | MIT |
| vitest | MIT |
| eslint | MIT |
| typescript-eslint | MIT / BSD-2-Clause |
| tailwindcss | MIT |
| postcss | MIT |
| autoprefixer | MIT |
| jsdom | MIT |
| globals | MIT |
| @eslint/js | MIT |
| @types/leaflet, @types/node | MIT (DefinitelyTyped) |

---

## Map data & geospatial services

Gable's delivery/routing features integrate with **OpenRouteService** (the
VROOM route optimizer and the **Pelias** geocoder), and the frontend renders
maps with **Leaflet**. The routing/geocoding results and any map tiles are
derived from open datasets that carry **attribution requirements**. If you
enable these features, you are responsible for displaying the required
attribution in the UI.

**Minimum attribution (always required when this data is shown):**

> © OpenStreetMap contributors

OpenStreetMap data is licensed under the **Open Database License (ODbL) 1.0**.
See <https://www.openstreetmap.org/copyright>.

**Additional Pelias source attribution (CC-BY):** the Pelias geocoder blends
multiple open datasets. Where results are derived from them, their attribution
must be preserved, including:

- **GeoNames** — Creative Commons Attribution (CC-BY) 4.0
- **Who's on First** — Creative Commons Attribution (CC-BY) 4.0
- **OpenAddresses** — attribution per each contributing source
- **OpenStreetMap** — ODbL 1.0 (as above)

**Software licenses of the services themselves:** OpenRouteService, VROOM, and
Pelias are open-source projects with their own licenses (e.g. GPL/MIT family) at
their respective repositories; those licenses govern the service software, while
the **data** licenses (ODbL / CC-BY) above govern attribution of the results
Gable displays.

**Map tiles:** if you configure Leaflet to load OpenStreetMap-based raster
tiles, the same `© OpenStreetMap contributors` attribution applies, plus any
terms of your chosen tile provider.
