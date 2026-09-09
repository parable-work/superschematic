// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';

// Deployed to GitHub Pages from .github/workflows/release.yml (job docs-deploy)
// at https://parable-work.github.io/superschematic/. `base` must match the
// repository name; every in-content link is written with the base included.
export default defineConfig({
  site: 'https://parable-work.github.io',
  base: '/superschematic',
  integrations: [
    starlight({
      title: 'superschematic',
      description:
        'A schema compiler: write one schema, generate SQL, ORM, REST, types and SDKs.',
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/parable-work/superschematic',
        },
      ],
      // src/content/docs/404.md is the not-found page and is rendered by the
      // docs route; Starlight's own injected /404 route would conflict with it.
      disable404Route: true,
      editLink: {
        baseUrl: 'https://github.com/parable-work/superschematic/edit/main/docs/',
      },
      sidebar: [
        { label: 'Overview', link: '/' },
        { label: 'Quickstart', items: [{ autogenerate: { directory: 'install' } }] },
        { label: 'Guides', items: [{ autogenerate: { directory: 'guides' } }] },
        { label: 'Reference', items: [{ autogenerate: { directory: 'reference' } }] },
      ],
      // Fails the build on a broken internal link or anchor.
      plugins: [starlightLinksValidator()],
    }),
  ],
});
