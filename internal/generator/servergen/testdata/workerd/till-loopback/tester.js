// The tester of till.capnp: a checkout reaches ts-pricing's quote in
// process, with Till's loopback credential, which ts-pricing admits as
// Till; a quote asked for from outside the Worker with no credential is
// refused with 401, as is one with a token Till's key did not sign.

async function expect(response, status, body) {
  const text = await response.text();
  if (response.status !== status || (body !== undefined && !text.includes(body))) {
    throw new Error(`${response.url}: ${response.status} ${text}, want ${status}${body === undefined ? '' : ' with ' + body}`);
  }
  return text;
}

export default {
  async test(_controller, env) {
    await expect(await env.TILL.fetch('https://Till/readyz'), 200, '"ready"');
    await expect(await env.TILL.fetch('https://Till/api/checkouts/tea'), 200, '"cents":300');
    await expect(await env.TILL.fetch('https://Till/api/quotes/tea'), 401);
    const forged = 'eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJUaWxsIiwic3ViIjoiVGlsbCIsImF1ZCI6IlRpbGwifQ.' + 'A'.repeat(86);
    await expect(await env.TILL.fetch('https://Till/api/quotes/tea', { headers: { 'Service-Authorization': 'Bearer ' + forged } }), 401);
    console.log('Till under workerd: the in-process call carried the loopback credential');
  },
};
