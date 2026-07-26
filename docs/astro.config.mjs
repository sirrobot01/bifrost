import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://bifrost.biodun.dev',
  integrations: [
    starlight({
      title: 'Bifrost',
      description: 'IPv6-native ingress for self-hosted services.',
      favicon: '/favicon.svg',
      // The mark carries no text, so one asset reads correctly in both themes
      // and the site title beside it picks up the theme colour on its own.
      logo: {
        src: './src/assets/bifrost-logo.svg',
        alt: 'Bifrost',
      },
      // Starlight already emits og:title, og:description, og:url and og:type.
      // Only the card image needs supplying. It is rendered from
      // src/assets/bifrost-og.svg at the 1200x630 that large cards expect.
      head: [
        {
          tag: 'meta',
          attrs: { property: 'og:image', content: 'https://bifrost.biodun.dev/bifrost-og.png' },
        },
        {
          tag: 'meta',
          attrs: { property: 'og:image:width', content: '1200' },
        },
        {
          tag: 'meta',
          attrs: { property: 'og:image:height', content: '630' },
        },
        {
          tag: 'meta',
          attrs: { property: 'og:image:alt', content: 'Bifrost — IPv6-native ingress for self-hosted services' },
        },
        {
          tag: 'meta',
          attrs: { name: 'twitter:card', content: 'summary_large_image' },
        },
        {
          tag: 'meta',
          attrs: { name: 'twitter:image', content: 'https://bifrost.biodun.dev/bifrost-og.png' },
        },
      ],
      lastUpdated: true,
      editLink: {
        baseUrl: 'https://github.com/sirrobot01/bifrost/edit/main/docs/src/content/docs/',
      },
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/sirrobot01/bifrost',
        },
      ],
      sidebar: [
        {
          label: 'Start here',
          items: [
            { slug: 'getting-started/quickstart' },
            { slug: 'getting-started/decision-guide' },
            { slug: 'getting-started/installation' },
            { slug: 'getting-started/troubleshooting' },
          ],
        },
        {
          label: 'Configure Bifrost',
          items: [{ slug: 'guides/configuration' }],
        },
        {
          label: 'Network',
          items: [
            { slug: 'networking/firewall' },
            { slug: 'networking/edge' },
          ],
        },
        {
          label: 'Applications',
          items: [
            { slug: 'applications/jellyfin' },
            { slug: 'applications/immich' },
            { slug: 'applications/plex' },
          ],
        },
        {
          label: 'Operations',
          items: [
            { slug: 'operations/security' },
            { slug: 'operations/releasing' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { slug: 'reference/edge-protocol' },
            { slug: 'reference/external-probe' },
          ],
        },
      ],
    }),
  ],
});
