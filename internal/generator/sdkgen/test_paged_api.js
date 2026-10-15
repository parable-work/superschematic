import { afterAll, describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of fixture-paged-api, with Catalog.listShelfItems
// added, against a server that pages as D60's routers do. SDK_DIR is the SDK
// package, with its types package linked into node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { FixturePagedApiSDK, ApiError, ValidationError } = await import(
  pathToFileURL(path.join(sdkDir, "index.ts")).href
);
const { invokeTool } = await import(pathToFileURL(path.join(sdkDir, "tools", "index.ts")).href);

const names = ["anvil", "bucket", "crate", "drum", "easel"];
const tags = ["blue", "green", "red"];
const shelfId = "5d0c7e2a-1b3f-4a6d-8c9e-0f1a2b3c4d5e";

// cursorOf is the cursor after the item at position (1-based): the unpadded
// base64url of its compact JSON, as page_parity.json's fromRows writes it
// ("Mg" after the second).
function cursorOf(position) {
  return Buffer.from(JSON.stringify(position)).toString("base64url");
}

function positionOf(cursor) {
  return JSON.parse(Buffer.from(cursor, "base64url").toString("utf8"));
}

// The server pages names, as items, and tags. state.repeat makes a page's
// next the cursor it was asked with; state.refuse is a cursor it answers
// with the 400 of a cursor no page returned.
const state = { requests: [], repeat: false, refuse: "" };

const server = Bun.serve({
  port: 0,
  fetch(request) {
    const url = new URL(request.url);
    state.requests.push({ path: url.pathname, query: [...url.searchParams] });
    let defaultLimit = 2;
    let maxLimit = 5;
    let count = names.length;
    const isTags = url.pathname === "/api/tags";
    if (isTags) {
      defaultLimit = 20;
      maxLimit = 100;
      count = tags.length;
    } else if (url.pathname !== "/api/items" && url.pathname !== `/api/shelves/${shelfId}/items`) {
      return new Response("not found", { status: 404 });
    }
    let limit = defaultLimit;
    const limitText = url.searchParams.get("limit");
    if (limitText !== null) {
      limit = Number(limitText);
      if (!Number.isInteger(limit) || limit < 1 || limit > maxLimit) {
        return Response.json(
          { status: 400, code: "bad_request", detail: `Invalid query parameter limit: must be at most ${maxLimit}` },
          { status: 400 }
        );
      }
    }
    const cursor = url.searchParams.get("cursor");
    let after = 0;
    if (cursor !== null) {
      try {
        after = positionOf(cursor);
      } catch {
        after = NaN;
      }
      if (!Number.isInteger(after) || cursor === state.refuse) {
        return Response.json(
          { status: 400, code: "bad_request", detail: "cursor is not one this operation returned" },
          { status: 400 }
        );
      }
    }
    const items = [];
    for (let position = after + 1; position <= count && items.length < limit; position++) {
      items.push(
        isTags
          ? tags[position - 1]
          : {
              id: `00000000-0000-4000-8000-${String(position).padStart(12, "0")}`,
              name: names[position - 1],
              sku: `${names[position - 1]}-sku`,
              inStock: position % 2 === 1,
            }
      );
    }
    let next = after + items.length < count ? cursorOf(after + items.length) : null;
    if (state.repeat && cursor !== null) {
      next = cursor;
    }
    return Response.json({ data: { items, next }, meta: { requestId: "req-1" } });
  },
});

afterAll(() => server.stop(true));

function sdk() {
  state.requests = [];
  state.repeat = false;
  state.refuse = "";
  return new FixturePagedApiSDK({ baseUrl: `http://localhost:${server.port}` });
}

const queries = () => state.requests.map((request) => request.query);

async function collect(iterable) {
  const items = [];
  for await (const item of iterable) {
    items.push(typeof item === "string" ? item : item.name);
  }
  return items;
}

async function validationErrors(promise) {
  try {
    await promise;
  } catch (err) {
    expect(err).toBeInstanceOf(ValidationError);
    return err.errors;
  }
  throw new Error("expected a ValidationError");
}

describe("TypeScript SDK paged lists", () => {
  test("the first page, the next and the last", async () => {
    const client = sdk();
    const first = await client.catalog.listItems();
    // The vectors' fromRows: five rows two to a page, next "Mg".
    expect(first.items.map((item) => item.name)).toEqual(["anvil", "bucket"]);
    expect(first.next).toBe("Mg");
    expect(first.items[0].sku).toBe("anvil-sku");
    expect(first.items[0].inStock).toBe(true);

    const second = await client.catalog.listItems({ inStock: true, cursor: first.next, limit: 2 });
    expect(second.items.map((item) => item.name)).toEqual(["crate", "drum"]);
    expect(second.next).toBe(cursorOf(4));

    const last = await client.catalog.listItems({ cursor: second.next, limit: 5 });
    expect(last.items.map((item) => item.name)).toEqual(["easel"]);
    expect(last.next).toBeNull();

    expect(queries()).toEqual([
      [],
      [
        ["inStock", "true"],
        ["cursor", "Mg"],
        ["limit", "2"],
      ],
      [
        ["cursor", cursorOf(4)],
        ["limit", "5"],
      ],
    ]);
  });

  test("a limit out of bounds is refused before the request", async () => {
    const client = sdk();
    expect(await validationErrors(client.catalog.listItems({ limit: 6 }))).toEqual({
      limit: [{ validator: "max", message: "limit must be at most 5." }],
    });
    expect(await validationErrors(client.catalog.listItems({ limit: 0 }))).toEqual({
      limit: [{ validator: "min", message: "limit must be at least 1." }],
    });
    expect(await validationErrors(client.catalog.listItems({ limit: 2.5 }))).toEqual({
      limit: [{ validator: "type", message: "limit must be a whole number." }],
    });
    expect(await validationErrors(client.catalog.listTags({ limit: 101 }))).toEqual({
      limit: [{ validator: "max", message: "limit must be at most 100." }],
    });
    expect(state.requests).toEqual([]);
  });

  test("listItemsAll collects every item, a page at a time", async () => {
    const client = sdk();
    expect(await collect(client.catalog.listItemsAll({ limit: 2 }))).toEqual(names);
    expect(queries()).toEqual([
      [["limit", "2"]],
      [
        ["limit", "2"],
        ["cursor", "Mg"],
      ],
      [
        ["limit", "2"],
        ["cursor", cursorOf(4)],
      ],
    ]);

    // It starts at the cursor it is given.
    sdk();
    expect(await collect(client.catalog.listItemsAll({ cursor: cursorOf(3) }))).toEqual(["drum", "easel"]);
  });

  test("listItemsAll asks for a page only when the loop reaches it", async () => {
    const client = sdk();
    const items = client.catalog.listItemsAll();
    expect(state.requests).toEqual([]);
    const taken = [];
    for await (const item of items) {
      taken.push(item.name);
      if (taken.length === 3) {
        break;
      }
    }
    expect(taken).toEqual(["anvil", "bucket", "crate"]);
    expect(state.requests).toHaveLength(2);
  });

  test("a page that fails ends the iteration with its error", async () => {
    const client = sdk();
    state.refuse = cursorOf(4);
    const taken = [];
    let failure;
    try {
      for await (const item of client.catalog.listItemsAll()) {
        taken.push(item.name);
      }
    } catch (err) {
      failure = err;
    }
    expect(taken).toEqual(names.slice(0, 4));
    expect(failure).toBeInstanceOf(ApiError);
    expect(failure.statusCode).toBe(400);
    expect(failure.message).toBe("cursor is not one this operation returned");
  });

  test("a page whose next is the cursor it was sent ends the iteration", async () => {
    const client = sdk();
    state.repeat = true;
    const taken = [];
    let failure;
    try {
      for await (const item of client.catalog.listItemsAll()) {
        taken.push(item.name);
      }
    } catch (err) {
      failure = err;
    }
    expect(taken).toEqual(["anvil", "bucket", "crate", "drum"]);
    expect(failure?.message).toBe("Catalog.listItems returned the cursor it was sent");
    expect(state.requests).toHaveLength(2);
  });

  test("a page of a primitive", async () => {
    const client = sdk();
    const page = await client.catalog.listTags({ limit: 2 });
    expect(page).toEqual({ items: ["blue", "green"], next: "Mg" });
    expect(await collect(client.catalog.listTagsAll())).toEqual(tags);
    expect(queries()).toEqual([[["limit", "2"]], []]);
  });

  test("a path parameter and body arguments beside the page", async () => {
    let client = sdk();
    const page = await client.catalog.listShelfItems(shelfId, "acme", undefined, { limit: 3 });
    expect(page.items.map((item) => item.name)).toEqual(names.slice(0, 3));
    expect(await collect(client.catalog.listShelfItemsAll(shelfId, "acme", "red", { cursor: page.next, limit: 1 }))).toEqual(
      names.slice(3)
    );
    // The object call takes the query parameters second.
    expect(await collect(client.catalog.listShelfItemsAll(shelfId, { maker: "acme" }, { limit: 4 }))).toEqual(names);
    expect(state.requests.every((request) => request.path === `/api/shelves/${shelfId}/items`)).toBe(true);
    expect(queries()).toEqual([
      [
        ["limit", "3"],
        ["maker", "acme"],
      ],
      [
        ["cursor", cursorOf(3)],
        ["limit", "1"],
        ["maker", "acme"],
        ["color", "red"],
      ],
      [
        ["cursor", cursorOf(4)],
        ["limit", "1"],
        ["maker", "acme"],
        ["color", "red"],
      ],
      [
        ["limit", "4"],
        ["maker", "acme"],
      ],
      [
        ["limit", "4"],
        ["cursor", cursorOf(4)],
        ["maker", "acme"],
      ],
    ]);
  });

  test("invokeTool passes cursor and limit and returns the page", async () => {
    const client = sdk();
    const page = await invokeTool(client, "catalog.listItems", { inStock: false, cursor: "Mg", limit: 2 });
    expect(page.items.map((item) => item.name)).toEqual(["crate", "drum"]);
    expect(page.next).toBe(cursorOf(4));
    const tagsPage = await invokeTool(client, "catalog.listTags", { limit: 1 });
    expect(tagsPage).toEqual({ items: ["blue"], next: cursorOf(1) });
    expect(queries()).toEqual([
      [
        ["inStock", "false"],
        ["cursor", "Mg"],
        ["limit", "2"],
      ],
      [["limit", "1"]],
    ]);
  });
});
