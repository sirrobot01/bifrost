import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://bifrost.biodun.dev',
  integrations: [
    starlight({
      title: 'Bifrost',
      description: 'IPv6-native ingress for self-hosted services.',
      favicon: '/favicon.svg',
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
            { slug: 'getting-started/installation' },
            { slug: 'getting-started/decision-guide' },
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
