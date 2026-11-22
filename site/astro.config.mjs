import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

export default defineConfig({
  site: 'https://job-sniper.pages.dev',
  integrations: [sitemap()],
});
