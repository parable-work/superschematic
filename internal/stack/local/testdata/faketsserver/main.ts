// A fake TypeScript server that honours the contract of a generated
// entrypoint (docs/stack-model.md, sections 8.1 and 8.6) for the local
// target's integration test: Bun runs it, it listens on $PORT, answers
// /healthz and /readyz, echoes its environment at /env, and on SIGTERM
// stops and says so.
const server = Bun.serve({
  port: Number(process.env.PORT),
  hostname: '127.0.0.1',
  fetch(request) {
    const path = new URL(request.url).pathname;
    if (path === '/healthz' || path === '/readyz') return new Response('ok');
    if (path === '/env') return Response.json(process.env);
    return new Response('not found', { status: 404 });
  },
});
console.log(`fake TypeScript server listening on 127.0.0.1:${server.port}`);
process.on('SIGTERM', () => {
  server.stop();
  console.log('fake TypeScript server stopped');
  process.exit(0);
});
