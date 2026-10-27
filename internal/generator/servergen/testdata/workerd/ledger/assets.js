// A stand-in for ts-ledger-web's static assets under workerd, which has
// none of its own: the build's one page by its path, matched exactly, as
// the assets match it with htmlHandling none, and 404 for any other path,
// as notFoundHandling none answers.

const page = '<!doctype html><title>ledger</title><div id=app></div>\n';

export default {
  async fetch(request) {
    if (new URL(request.url).pathname !== '/index.html') return new Response(null, { status: 404 });
    return new Response(request.method === 'HEAD' ? null : page, { headers: { 'content-type': 'text/html' } });
  },
};
