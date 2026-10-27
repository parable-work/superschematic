// A stand-in for the internet a site's Worker reaches an API of its origin
// on another provider through (D91): the site's globalOutbound, so its
// fetch of the API's public address lands here. It answers with what it
// saw: the URL, the Host the fetch carried, and the forwarded headers.

export default {
  async fetch(request) {
    const url = new URL(request.url);
    return Response.json({
      url: url.href,
      host: url.host,
      forwardedHost: request.headers.get('x-forwarded-host'),
      forwardedProto: request.headers.get('x-forwarded-proto'),
      forwardedPrefix: request.headers.get('x-forwarded-prefix'),
    });
  },
};
