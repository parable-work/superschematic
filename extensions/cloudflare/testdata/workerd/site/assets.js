// A stand-in for a site Worker's static assets under workerd, which has
// none of its own: the files of a site's build by path, matched exactly, as
// the assets match them with htmlHandling none, and 404 for any other
// path, as notFoundHandling none answers. A HEAD gets the headers alone.

const files = {
  '/index.html': ['text/html', '<!doctype html><title>storefront</title><div id=app></div>\n'],
  '/docs/index.html': ['text/html', '<!doctype html><title>docs</title>\n'],
  '/app.js': ['text/javascript', 'console.log("storefront");\n'],
};

export default {
  async fetch(request) {
    const { pathname } = new URL(request.url);
    const file = files[pathname];
    if (!file) return new Response(null, { status: 404 });
    const [type, body] = file;
    return new Response(request.method === 'HEAD' ? null : body, {
      headers: { 'content-type': type, etag: `"${body.length}"`, 'cache-control': 'public, max-age=0, must-revalidate' },
    });
  },
};
