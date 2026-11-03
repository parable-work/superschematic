import { HttpMethod, app, rest } from "@superschematic/api";

export abstract class StockView {
  id: string;
}

export class StockQueries {
  @rest(HttpMethod.GET, "stock/{id}")
  getStock(id: string): StockView {
    throw new Error("schema declaration only");
  }
}

// The staff's console over the stock, with its pages.
@app({ extensions: ["topcoat"] })
export abstract class Backoffice {}

// A kiosk of the project's own.
@app()
export abstract class Kiosk {}
