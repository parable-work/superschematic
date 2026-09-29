import { ChainCountry } from "@schemas/fixture-chain-base";

// A postal address. Its country comes from fixture-chain-base, so every Go
// module built on this types module reaches that one too.
export type Address = {
  readonly street: string;
  readonly country: ChainCountry;
};
