# cernly.dev

The developer docs. Markdown pages in `pages/`, built with:

    go run ./cmd/cernly-site     # writes site/public

Each page starts with front matter (`title`, `description`, `path`, `section`,
`order`, `commands`) and opens with a one-line **TL;DR**. The build writes the
HTML, each page's Markdown at `<path>index.md`, `llms.txt`, `llms-full.txt`,
`sitemap.xml`, JSON-LD, and copies `schema/` to `/schema/` and the agent
skills in `skills/` to `/skills/`. `agents.md` holds the notes for AI agents
that open `llms.txt` and `llms-full.txt`. A link to a page that does not exist
fails the build.

Hosted on a VPS behind Cloudflare at https://cernly.dev, as static files.
