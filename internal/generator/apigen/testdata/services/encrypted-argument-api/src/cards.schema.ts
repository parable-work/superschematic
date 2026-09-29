// An EncryptedField<T> argument outside an Encrypted operation set. The Go
// route tests in apigen and the SDK generator tests load it.
import { Nullable } from "@superschematic/schema";
import { EncryptedField, HttpMethod, rest } from "@superschematic/api";

// What a card operation returns.
export abstract class CardReceipt {
  customerId: string;
  label: string;
}

// The set is not Encrypted. storeCard is encrypted because its number
// argument is an EncryptedField<string>; renameCard is a plain JSON route.
export class CardMutations {
  // Store a card for a customer.
  @rest(HttpMethod.POST, "customers/{customerId}/cards")
  storeCard(customerId: string, number: EncryptedField<string>, label: Nullable<string>): CardReceipt {
    throw new Error("schema declaration only");
  }

  // Rename a card.
  @rest(HttpMethod.PATCH, "cards/{id}")
  renameCard(id: string, label: string): CardReceipt {
    throw new Error("schema declaration only");
  }
}
