import type { APIRoute } from 'astro';

export const GET: APIRoute = ({ site }) => {
  const sitemapUrl = site ? new URL('/sitemap-index.xml', site).href : '/sitemap-index.xml';
  const body = `User-agent: *\nAllow: /\n\nSitemap: ${sitemapUrl}`;

  return new Response(body, {
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  });
};
