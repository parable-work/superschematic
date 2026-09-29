// An Encrypted operation set whose operations take an input type and scalar
// arguments. The Go SDK test sends each body through the generated Go server
// and its payload decryptor; the Rust SDK test opens the envelopes itself.
// Both check that the plaintext is the request body, not a wrapper around it.
import { Nullable } from "@superschematic/schema";
import { Encrypted, HttpMethod, rest } from "@superschematic/api";

// What a wallet operation returns.
export abstract class WalletReceipt {
  owner: string;
  hint: Nullable<string>;
}

// A wallet to open.
export abstract class OpenWalletInput {
  owner: string;
  pin: string;
}

export class WalletMutations extends Encrypted {
  // Open a wallet.
  @rest(HttpMethod.POST, "wallets")
  openWallet(input: OpenWalletInput): WalletReceipt {
    throw new Error("schema declaration only");
  }

  // Reset a wallet's pin.
  @rest(HttpMethod.PUT, "wallets/{id}/pin")
  resetPin(id: string, pin: string, hint: Nullable<string>): WalletReceipt {
    throw new Error("schema declaration only");
  }
}
