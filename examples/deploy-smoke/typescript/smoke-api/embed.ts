// A name's embedding in three dimensions, in place of a model's: three
// 32-bit FNV-1a hashes of the name, each seeded apart, scaled to [-1, 1].
// The same name always embeds alike, so a line item is its own nearest
// neighbour, at a cosine distance of 0.
export function embed(name: string): number[] {
  return [0x811c9dc5, 0x01000193, 0x9e3779b9].map(seed => {
    let hash = seed;
    for (let i = 0; i < name.length; i++) {
      hash ^= name.charCodeAt(i);
      hash = Math.imul(hash, 0x01000193) >>> 0;
    }
    return (hash / 0xffffffff) * 2 - 1;
  });
}
