import { Finance, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { Authenticated, HttpMethod, rest, source } from "@superschematic/api";
import { TransactionListRow } from "@schemas/fixture-finances-db";

// The dashboard's transaction list as the API returns it (D90): a view
// whose @source is a projection, which reads the projection's columns as a
// table's view reads a table's.
@source(TransactionListRow)
export abstract class TransactionView {
  id: Identity.UUID;
  date: Temporal.DateTime;
  name: string;
  amount: Finance.SignedMoney;
  accountName: Identity.Name;
  categoryName: Nullable<Identity.Name>;
  parentCategoryName: Nullable<Identity.Name>;
}

export class Transactions extends Authenticated {
  // The household's transactions, newest first.
  @rest(HttpMethod.GET, "transactions")
  listTransactions(): TransactionView[] {
    throw new Error("schema declaration only");
  }
}
