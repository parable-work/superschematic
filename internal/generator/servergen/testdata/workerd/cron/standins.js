// The stand-in for a Secrets Store secret binding, which workerd does not
// provide on its own: cron.capnp binds it as a wrapped binding, and
// workerd calls this with its inner binding, the secret's value.

/** A Secrets Store secret whose value is its inner binding value. */
export function secret(env) {
  return { get: async () => env.value };
}
