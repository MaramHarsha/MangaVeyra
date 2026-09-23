# Screen inventory → routes

Every screen from the design, labelled `ID – Name – Platform`, with the route that implements it in this repository.
Mobile artboards (390 × 844) are the same routes rendered responsively. Screens marked **World platform (repo 2)** are
hosted by the closed World platform and are *not* built here; the app only links to them.

| Screen | Design file | Route / surface | Module |
|---|---|---|---|
| D1 – Editor – Desktop | 05 | /studio/p/{project}/chapters/{n}/edit?page=7 (editor island) | `editor` |
| D8 – Generation & variations – Desktop | 05 | editor: variations tray | `editor/gen` |
| D13 – Region edit – Desktop | 05 | editor: region edit (?tool=region) | `editor/gen` |
| D3 – Lettering – Desktop | 05 | editor: Lettering mode (?mode=lettering) | `editor/lettering` |
| E2 – Export – Desktop | 05 | editor export modal + /studio/p/{project}/chapters/{n}/export | `export` |
| D9 – Mobile editor – Mobile | 05 | editor island, mobile layout | `editor` |
| D13 – Region edit – Mobile | 05 | editor: region edit (?tool=region) | `editor/gen` |
| D9 – Reorder pages – Mobile | 05 | editor island, mobile layout | `editor` |
| G8 – First-run setup – Desktop | 06 | /setup/{admin|instance|models|finish} | `setup` |
| B1 – Dashboard – Desktop | 06 | /studio | `studio` |
| A1 – Landing – Desktop | 06 | site/index.html (static project website) · /about in-app copy | `site` |
| A3 – Sign up – Desktop | 06 | /signup?invite=… | `auth` |
| A5 – Onboarding – Desktop | 06 | /onboarding/{1|2|3} | `auth` |
| A1 – Landing – Mobile | 06 | site/index.html (static project website) · /about in-app copy | `site` |
| A2 – Log in – Mobile | 06 | /login | `auth` |
| A4 – Forgot password – Mobile | 06 | /forgot, /reset/{token} | `auth` |
| A5 – Onboarding – Mobile | 06 | /onboarding/{1|2|3} | `auth` |
| B1 – Dashboard – Mobile | 06 | /studio | `studio` |
| B3 – New project wizard – Desktop | 07 | /studio/new/{step} | `studio` |
| C1 – Story editor – Desktop | 07 | /studio/p/{project}/story[/{chapter}[/{scene}]] | `studio` |
| C4 – Create character – Desktop | 07 | /studio/p/{project}/characters/new | `studio` |
| C2 – Character bible – Desktop | 07 | /studio/p/{project}/characters | `studio` |
| C3 – Character detail – Desktop | 07 | /studio/p/{project}/characters/{id} | `studio` |
| C5 – World-building – Desktop | 07 | /studio/p/{project}/setting | `studio` |
| C6 – Continuity report – Desktop | 07 | /studio/p/{project}/chapters/{n}/continuity | `gen` |
| B2 – Projects – Desktop | 07 | /studio/projects | `studio` |
| B4 – Templates – Desktop | 07 | /studio/templates | `studio` |
| B3 – New project wizard – Mobile | 07 | /studio/new/{step} | `studio` |
| C2 – Character bible – Mobile | 07 | /studio/p/{project}/characters | `studio` |
| C3 – Character detail – Mobile | 07 | /studio/p/{project}/characters/{id} | `studio` |
| E1 – Publish flow – Desktop | 08 | /studio/p/{project}/chapters/{n}/publish, …/published | `publish` |
| E1 – Published – Desktop | 08 | /studio/p/{project}/chapters/{n}/publish, …/published | `publish` |
| E5 – Series page – Desktop | 08 | /s/{series} | `publish/reader` |
| E3 – Reader, paged – Desktop | 08 | /s/{series}/{chapter} (paged, spread, guided) | `reader` |
| E1 – Publish – Mobile | 08 | /studio/p/{project}/chapters/{n}/publish, …/published | `publish` |
| E5 – Series page – Mobile | 08 | /s/{series} | `publish/reader` |
| E4 – Reader, webtoon – Mobile | 08 | /s/{series}/{chapter} (webtoon scroll) | `reader` |
| E3 – Reader, guided – Mobile | 08 | /s/{series}/{chapter} (paged, spread, guided) | `reader` |
| D5 – Translation mode – Desktop | 08 | /studio/p/{project}/chapters/{n}/translate/{lang} | `translate` |
| E6 – Explore – Desktop | 08 | /explore | `discover` |
| E8 – Search – Desktop | 08 | /search?q= | `discover` |
| F1 – Creator profile – Desktop | 08 | /u/{username} | `social` |
| F2 – Analytics – Desktop | 08 | /studio/analytics[?series=] | `analytics` |
| G4 – Notifications – Desktop | 08 | /notifications | `notify` |
| E6 – Explore – Mobile | 08 | /explore | `discover` |
| E7 – Library – Mobile | 08 | /library/{following|history|downloads|bookmarks} | `reader` |
| F1 – Creator profile – Mobile | 08 | /u/{username} | `social` |
| G4 – Notifications – Mobile | 08 | /notifications | `notify` |
| G14 – Update available – Desktop | 09 | update modal at admin login + /admin/update, member banner | `update` |
| G14 – Updating – Desktop | 09 | update modal at admin login + /admin/update, member banner | `update` |
| G14 – Update done – Desktop | 09 | update modal at admin login + /admin/update, member banner | `update` |
| G14 – Update failed, restored – Desktop | 09 | update modal at admin login + /admin/update, member banner | `update` |
| G14 – Update available – Mobile | 09 | update modal at admin login + /admin/update, member banner | `update` |
| D12 – Character Lock fix – Desktop | 09 | editor: Character Lock fix (?tool=lockfix&character=) | `gen` |
| D2 – Layout picker – Desktop | 09 | editor: layout picker (?panel=layouts) | `editor` |
| D4 – Sketch to manga – Desktop | 09 | editor: sketch to manga (?tool=sketch) | `editor/gen` |
| D6 – Animate & living panels – Desktop | 09 | editor: Animate mode (?mode=animate) | `animate` |
| D7 – Collaboration & review – Desktop | 09 | editor: review drawer (?panel=review) | `editor` |
| D10 – Version history – Desktop | 09 | /studio/p/{project}/chapters/{n}/pages/{page}/history | `editor` |
| D11 – Format reflow – Desktop | 09 | /studio/p/{project}/reflow (v2) | `editor` |
| G2 – Models & providers – Desktop | 09 | /models | `ai` |
| G11 – Model routing – Desktop | 09 | /models/routing | `ai` |
| G12 – Add custom provider – Desktop | 09 | /models/providers/new/{step} | `ai` |
| G13 – Agents & MCP – Desktop | 09 | /agents | `mcp` |
| G9 – Job queue – Desktop | 09 | /queue | `jobs` |
| G1 – Settings – Desktop | 09 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G10 – Backup & import – Desktop | 09 | /admin/backup | `backup/importer` |
| G7 – Plugins – Desktop | 09 | /plugins | `plugins` |
| G6 – 404 – Desktop | 09 | 404 handler, /offline (service worker), maintenance gate | `web` |
| G6 – Offline – Desktop | 09 | 404 handler, /offline (service worker), maintenance gate | `web` |
| G6 – Maintenance – Desktop | 09 | 404 handler, /offline (service worker), maintenance gate | `web` |
| G6 – 404 – Mobile | 09 | 404 handler, /offline (service worker), maintenance gate | `web` |
| G6 – Offline – Mobile | 09 | 404 handler, /offline (service worker), maintenance gate | `web` |
| G1 – Settings – Mobile | 09 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G9 – Job queue – Mobile | 09 | /queue | `jobs` |
| H12 – World tab in the app – Desktop | 11 | /world | `world` |
| H12 – World tab – Mobile | 11 | /world | `world` |
| H9 – Choose World name – Desktop | 11 | /world/name (during linking) | `world` |
| H10 – World profile – Desktop | 11 | /world/profile | `world` |
| H8 – World connection – Desktop | 11 | /settings/world | `world` |
| H3 – Submit to World – Desktop | 11 | /world/submit?item={kind}:{id} (modal) | `world` |
| H4 – My submissions – Desktop | 11 | /world/submissions | `world` |
| H4 – My submissions – Mobile | 11 | /world/submissions | `world` |
| B2 – Projects – Mobile | 12 | /studio/projects | `studio` |
| B4 – Templates – Mobile | 12 | /studio/templates | `studio` |
| C1 – Story editor – Mobile | 12 | /studio/p/{project}/story[/{chapter}[/{scene}]] | `studio` |
| E8 – Search – Mobile | 12 | /search?q= | `discover` |
| F2 – Analytics – Mobile | 12 | /studio/analytics[?series=] | `analytics` |
| B1 – Dashboard – Desktop · Paper | 12 | /studio | `studio` |
| E5 – Series page – Mobile · Paper | 12 | /s/{series} | `publish/reader` |
| E6 – Explore – Mobile · Paper | 12 | /explore | `discover` |
| G1 – Settings · Account – Desktop | 13 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Language – Desktop | 13 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Reading – Desktop | 13 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Notifications – Desktop | 13 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Accessibility – Desktop | 13 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Data & export – Desktop | 13 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| A2 – Log in – Desktop | 13 | /login | `auth` |
| A4 – Forgot password – Desktop | 13 | /forgot, /reset/{token} | `auth` |
| E4 – Reader, webtoon – Desktop | 13 | /s/{series}/{chapter} (webtoon scroll) | `reader` |
| E7 – Library – Desktop | 13 | /library/{following|history|downloads|bookmarks} | `reader` |
| H11 – My World library – Desktop | 13 | /world/library | `world` |
| A3 – Sign up – Mobile | 14 | /signup?invite=… | `auth` |
| G1 – Settings · Account – Mobile | 14 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Appearance – Mobile | 14 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Language – Mobile | 14 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Reading – Mobile | 14 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Notifications – Mobile | 14 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Accessibility – Mobile | 14 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| G1 – Settings · Data & export – Mobile | 14 | /settings/{account|appearance|language|reading|notifications|accessibility|world|data} | `settings` |
| C4 – Create character – Mobile | 14 | /studio/p/{project}/characters/new | `studio` |
| D3 – Lettering – Mobile | 14 | editor: Lettering mode (?mode=lettering) | `editor/lettering` |
| E2 – Export – Mobile | 14 | editor export modal + /studio/p/{project}/chapters/{n}/export | `export` |
| D5 – Translation review – Mobile | 14 | /studio/p/{project}/chapters/{n}/translate/{lang} | `translate` |
| G2 – Models & providers – Mobile | 14 | /models | `ai` |
| G13 – Agents & MCP – Mobile | 14 | /agents | `mcp` |
| H9 – Choose World name – Mobile | 14 | /world/name (during linking) | `world` |
| H3 – Submit to World – Mobile | 14 | /world/submit?item={kind}:{id} (modal) | `world` |

## World platform screens (repo 2, not in this repository)

- F5 – Revenue split – Mobile (Platform)
- F5 – Revenue split – Platform
- G5 – Admin & moderation – Desktop
- G5 – Admin reports – Mobile
- G5 – Admin · Content rules – Desktop
- G5 – Admin · Server – Desktop
- G5 – Admin · Users – Desktop
- H1 – World directory – Desktop
- H1 – World directory – Mobile
- H2 – World item – Desktop
- H2 – World item – Mobile
- H5 – Review queue – Mobile (World admin)
- H5 – Review queue – World admin
- H6 – Review detail – World admin
- H7 – Developer account – Desktop
- P0 – World store home – Platform
- P0 – World store – Mobile (Platform)
- P1 – Earnings & monthly payout – Platform
- P1 – Earnings – Mobile (Platform)
- P10 – Fees & payouts page – Platform
- P10 – Fees & payouts – Mobile (Platform)
- P11 – Content policy – Platform
- P12 – Copyright takedown form – Platform
- P13 – Paid chapter preview – Mobile (Platform)
- P13 – Paid chapter preview – Platform
- P2 – Checkout – Mobile (Platform)
- P2 – Checkout – Platform
- P3 – Early-access chapter – Mobile (Platform)
- P3 – Early-access chapter – Platform
- P4 – Seller listing – Mobile (Platform)
- P4 – Seller listing – Platform
- P6 – Payout settings – Mobile (Platform)
- P6 – Payout settings – Platform
- P7 – Purchases – Mobile (Platform)
- P7 – Purchases – Platform
- P8 – Revenue & commission – Mobile (World admin)
- P8 – Revenue & commission – World admin
- P9 – Refunds & disputes – World admin
- W1 – World website home – Mobile (Platform)
- W1 – World website home – Platform
- W2 – Browse category – Platform
- W3 – Item page – Mobile (Platform)
- W3 – Item page – Platform
- W4 – Creator shop – Platform
- W5 – Series on World – Platform
- W6 – Sell on World – Platform
- W7 – Sign in to World – Platform
- WA1 – World Admin overview – Admin
- WA10 – IP blocking & rate limits – Admin
- WA11 – Paid content protection – Admin
- WA12 – Integrations & keys – Admin
- WA13 – Admin access – Admin
- WA14 – Audit log – Admin
- WA15 – Leak tracing – Admin
- WA16 – Leak cases – Admin
- WA17 – Ban email – Email
- WA17 – Email templates – Admin
- WA2 – Items & listings – Admin
- WA9 – Blocked words – Admin
