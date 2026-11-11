import { afterAll, describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of fixture-paged-api, with Catalog.listShelfItems
// and Catalog.findParts added, against a server that pages as D60's routers
// do and keeps the parts a request's filters find (D106). SDK_DIR is the SDK
// package, with its types package linked into node_modules, and
// FILTER_PARITY runtime/http/testdata/filter_parity.json.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}
const filterParity = process.env.FILTER_PARITY;
if (!filterParity) {
  throw new Error("FILTER_PARITY env var required");
}

const { FixturePagedApiSDK, ApiError, ValidationError } = await import(
  pathToFileURL(path.join(sdkDir, "index.ts")).href
);
const { invokeTool } = await import(pathToFileURL(path.join(sdkDir, "tools", "index.ts")).href);
const { filterParams } = await import(pathToFileURL(path.join(sdkDir, "client.ts")).href);

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

// The sorts listItems offers (D60, amended), and the order of the names'
// positions under each: the skus sort as the names do.
const sorts = {
  name: [1, 2, 3, 4, 5],
  "-name": [5, 4, 3, 2, 1],
  sku: [1, 2, 3, 4, 5],
  "-sku": [5, 4, 3, 2, 1],
};

// sortedCursorOf is the cursor after the item at index (1-based) of a
// sorted page, which names its sort as a router binds it.
function sortedCursorOf(sort, index) {
  return Buffer.from(JSON.stringify({ sort, key: index })).toString("base64url");
}

// The parts Catalog.listParts and Catalog.findParts list: a field of every
// kind a filter compares (D106), retiredOn and supplierId null on some.
const supplier = "7c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f";
const partId = (position) => `00000000-0000-4000-8000-${String(position).padStart(12, "0")}`;
const parts = [
  ["axle", "bolt", 1, 0.5, "1.50", false, "2026-01-01T00:00:00Z", null, null],
  ["bracket", "nut", 2, 1.5, "2.50", true, "2026-02-01T00:00:00Z", "2026-06-01", supplier],
  ["clamp", "washer", 3, 2.5, "3.50", false, "2026-03-01T00:00:00Z", null, supplier],
  ["dowel", "bolt", 4, 3.5, "4.50", true, "2026-04-01T00:00:00Z", "2026-07-01", null],
  ["eyelet", "nut", 5, 4.5, "5.50", false, "2026-05-01T00:00:00Z", null, null],
].map(([name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId], index) => ({
  id: partId(index + 1),
  name,
  kind,
  quantity,
  weight,
  price,
  discontinued,
  madeAt,
  retiredOn,
  supplierId,
}));

// The sorts listParts offers, each a field and whether it descends.
const partSorts = { name: ["name", 1], "-name": ["name", -1], quantity: ["quantity", 1], "-quantity": ["quantity", -1] };

const filterOperators = ["eq", "in", "gt", "gte", "lt", "lte", "contains", "isNull"];

// keeps reports whether part passes one filter of a request, filter[field]
// or filter[field][op]: a test's reader, enough to tell the parts apart.
function keeps(part, field, op, text) {
  const actual = part[field];
  const typed = (value) => (typeof actual === "number" ? Number(value) : typeof actual === "boolean" ? value === "true" : value);
  switch (op) {
    case "eq":
      return actual === typed(text);
    case "in":
      return text.split(",").map(typed).includes(actual);
    case "gt":
      return actual !== null && actual > typed(text);
    case "gte":
      return actual !== null && actual >= typed(text);
    case "lt":
      return actual !== null && actual < typed(text);
    case "lte":
      return actual !== null && actual <= typed(text);
    case "contains":
      return typeof actual === "string" && actual.toLowerCase().includes(text.toLowerCase());
    default:
      return (actual === null) === (text === "true");
  }
}

// partsResponse answers /api/parts, a sorted page of the parts the
// request's filters keep, and /api/parts/all, all of them in one list. It
// refuses an operator no filter has, as a route does.
function partsResponse(url) {
  let kept = parts;
  for (const [key, text] of url.searchParams) {
    const match = /^filter\[([^\]]+)\](?:\[([^\]]+)\])?$/.exec(key);
    if (match === null) {
      continue;
    }
    const [, field, op = "eq"] = match;
    if (!filterOperators.includes(op)) {
      return Response.json(
        { status: 400, code: "bad_request", detail: `Invalid query parameter filter[${field}]: ${op} is no operator` },
        { status: 400 }
      );
    }
    kept = kept.filter((part) => keeps(part, field, op, text));
  }
  if (url.pathname === "/api/parts/all") {
    return Response.json({ data: kept, meta: { requestId: "req-1" } });
  }
  const sort = url.searchParams.get("sort") ?? "name";
  const [field, direction] = partSorts[sort];
  const sorted = [...kept].sort((a, b) => (a[field] < b[field] ? -direction : a[field] > b[field] ? direction : 0));
  const cursor = url.searchParams.get("cursor");
  const after = cursor === null ? 0 : positionOf(cursor).key;
  const limit = Number(url.searchParams.get("limit") ?? 2);
  const items = sorted.slice(after, after + limit);
  const last = after + items.length;
  return Response.json({
    data: { items, next: last < sorted.length ? sortedCursorOf(sort, last) : null },
    meta: { requestId: "req-1" },
  });
}

// The server pages names, as items, and tags. state.repeat makes a page's
// next the cursor it was asked with; state.refuse is a cursor it answers
// with the 400 of a cursor no page returned. A request to /api/items that
// sends a sort reads that order, refuses a sort it does not offer and a
// cursor of another sort, and binds its next to the sort.
const state = { requests: [], repeat: false, refuse: "" };

const server = Bun.serve({
  port: 0,
  fetch(request) {
    const url = new URL(request.url);
    state.requests.push({ path: url.pathname, query: [...url.searchParams] });
    if (url.pathname === "/api/parts" || url.pathname === "/api/parts/all") {
      return partsResponse(url);
    }
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
    const sort = url.pathname === "/api/items" ? url.searchParams.get("sort") : null;
    if (sort !== null && !(sort in sorts)) {
      return Response.json(
        { status: 400, code: "bad_request", detail: "Invalid query parameter sort: expected one of name, -name, sku, -sku" },
        { status: 400 }
      );
    }
    const cursor = url.searchParams.get("cursor");
    let after = 0;
    if (cursor !== null) {
      let bound = sort === null;
      try {
        const key = positionOf(cursor);
        if (sort === null) {
          after = key;
        } else if (key !== null && typeof key === "object") {
          bound = key.sort === sort;
          after = key.key;
        } else {
          after = NaN;
        }
      } catch {
        after = NaN;
      }
      if (!bound) {
        return Response.json(
          { status: 400, code: "bad_request", detail: "cursor is not one this sort returned" },
          { status: 400 }
        );
      }
      if (!Number.isInteger(after) || cursor === state.refuse) {
        return Response.json(
          { status: 400, code: "bad_request", detail: "cursor is not one this operation returned" },
          { status: 400 }
        );
      }
    }
    const order = sorts[sort ?? "name"];
    const items = [];
    for (let index = after + 1; index <= count && items.length < limit; index++) {
      const position = isTags ? index : order[index - 1];
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
    const last = after + items.length;
    let next = last < count ? (sort === null ? cursorOf(last) : sortedCursorOf(sort, last)) : null;
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

  test("listItemsAll sends the sort with every page", async () => {
    const client = sdk();
    expect(await collect(client.catalog.listItemsAll({ sort: "-name", limit: 2 }))).toEqual([...names].reverse());
    expect(queries()).toEqual([
      [
        ["sort", "-name"],
        ["limit", "2"],
      ],
      [
        ["sort", "-name"],
        ["limit", "2"],
        ["cursor", sortedCursorOf("-name", 2)],
      ],
      [
        ["sort", "-name"],
        ["limit", "2"],
        ["cursor", sortedCursorOf("-name", 4)],
      ],
    ]);
  });

  test("a sort is sent as it is, and the server refuses one it does not offer", async () => {
    const client = sdk();
    const page = await client.catalog.listItems({ sort: "-sku", limit: 1 });
    expect(page.items.map((item) => item.name)).toEqual(["easel"]);
    let failure;
    try {
      await client.catalog.listItems({ sort: "price" });
    } catch (err) {
      failure = err;
    }
    expect(failure).toBeInstanceOf(ApiError);
    expect(failure.statusCode).toBe(400);
    expect(failure.message).toBe("Invalid query parameter sort: expected one of name, -name, sku, -sku");

    // A cursor of one sort sent with another is the server's 400.
    failure = undefined;
    try {
      await client.catalog.listItems({ sort: "name", cursor: page.next });
    } catch (err) {
      failure = err;
    }
    expect(failure?.statusCode).toBe(400);
    expect(failure?.message).toBe("cursor is not one this sort returned");
    expect(queries()).toEqual([
      [
        ["sort", "-sku"],
        ["limit", "1"],
      ],
      [["sort", "price"]],
      [
        ["sort", "name"],
        ["cursor", sortedCursorOf("-sku", 1)],
      ],
    ]);
  });

  test("invokeTool passes the sort", async () => {
    const client = sdk();
    const page = await invokeTool(client, "catalog.listItems", { sort: "-name", limit: 2 });
    expect(page.items.map((item) => item.name)).toEqual(["easel", "drum"]);
    const next = await invokeTool(client, "catalog.listItems", { sort: "-name", cursor: page.next, limit: 2 });
    expect(next.items.map((item) => item.name)).toEqual(["crate", "bucket"]);
    expect(queries()).toEqual([
      [
        ["sort", "-name"],
        ["limit", "2"],
      ],
      [
        ["sort", "-name"],
        ["cursor", sortedCursorOf("-name", 2)],
        ["limit", "2"],
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

  test("a filter of every kind and operator is written as the bracket form", async () => {
    const client = sdk();
    const page = await client.catalog.listParts({
      filter: {
        id: partId(1),
        name: { contains: "X", in: ["axle", "bracket"], eq: "axle" },
        kind: { in: ["bolt", "nut"] },
        // The operators travel in the routes' order, whatever the object's.
        quantity: { lte: 8, lt: 9, gte: 1, gt: 0, in: [1, 2], eq: 1 },
        weight: { lt: 2.5 },
        price: { gte: "1.00", lte: "9.99" },
        discontinued: false,
        madeAt: { gt: "2025-12-31T00:00:00Z" },
        retiredOn: { isNull: true },
        supplierId: { isNull: true },
      },
    });
    expect(page.items.map((part) => part.name)).toEqual(["axle"]);
    expect(page.next).toBeNull();
    expect(queries()).toEqual([
      [
        ["filter[id]", partId(1)],
        ["filter[name]", "axle"],
        ["filter[name][in]", "axle,bracket"],
        ["filter[name][contains]", "X"],
        ["filter[kind][in]", "bolt,nut"],
        ["filter[quantity]", "1"],
        ["filter[quantity][in]", "1,2"],
        ["filter[quantity][gt]", "0"],
        ["filter[quantity][gte]", "1"],
        ["filter[quantity][lt]", "9"],
        ["filter[quantity][lte]", "8"],
        ["filter[weight][lt]", "2.5"],
        ["filter[price][gte]", "1.00"],
        ["filter[price][lte]", "9.99"],
        ["filter[discontinued]", "false"],
        ["filter[madeAt][gt]", "2025-12-31T00:00:00Z"],
        ["filter[retiredOn][isNull]", "true"],
        ["filter[supplierId][isNull]", "true"],
      ],
    ]);
  });

  test("each kind's value and each operator keeps its parts", async () => {
    const client = sdk();
    const kept = async (filter) => (await client.catalog.listParts({ filter, limit: 5 })).items.map((part) => part.name);
    expect(await kept({ id: { in: [partId(2), partId(4)] } })).toEqual(["bracket", "dowel"]);
    expect(await kept({ name: { contains: "CK" } })).toEqual(["bracket"]);
    expect(await kept({ kind: "nut" })).toEqual(["bracket", "eyelet"]);
    expect(await kept({ quantity: { gt: 2, lte: 4 } })).toEqual(["clamp", "dowel"]);
    expect(await kept({ weight: { gte: 3.5 } })).toEqual(["dowel", "eyelet"]);
    expect(await kept({ price: { lt: "2.50" } })).toEqual(["axle"]);
    expect(await kept({ discontinued: { eq: true } })).toEqual(["bracket", "dowel"]);
    expect(await kept({ madeAt: { gte: "2026-04-01T00:00:00Z" } })).toEqual(["dowel", "eyelet"]);
    expect(await kept({ retiredOn: { isNull: false } })).toEqual(["bracket", "dowel"]);
    expect(await kept({ retiredOn: "2026-06-01" })).toEqual(["bracket"]);
    expect(await kept({ supplierId: { isNull: true }, kind: { in: ["bolt"] } })).toEqual(["axle", "dowel"]);
    expect(queries().map((query) => query.filter(([name]) => name !== "limit"))).toEqual([
      [["filter[id][in]", `${partId(2)},${partId(4)}`]],
      [["filter[name][contains]", "CK"]],
      [["filter[kind]", "nut"]],
      [
        ["filter[quantity][gt]", "2"],
        ["filter[quantity][lte]", "4"],
      ],
      [["filter[weight][gte]", "3.5"]],
      [["filter[price][lt]", "2.50"]],
      [["filter[discontinued]", "true"]],
      [["filter[madeAt][gte]", "2026-04-01T00:00:00Z"]],
      [["filter[retiredOn][isNull]", "false"]],
      [["filter[retiredOn]", "2026-06-01"]],
      [
        ["filter[supplierId][isNull]", "true"],
        ["filter[kind][in]", "bolt"],
      ],
    ]);
  });

  test("a member that is unset and an in of no value are left out", async () => {
    const client = sdk();
    const page = await client.catalog.listParts({
      filter: { id: undefined, name: null, kind: { in: [] }, quantity: { gt: undefined, lt: null }, weight: {} },
      limit: 5,
    });
    expect(page.items).toHaveLength(5);
    await client.catalog.listParts({ filter: {} });
    await client.catalog.listParts({ filter: undefined });
    await client.catalog.listParts();
    expect(queries()).toEqual([[["limit", "5"]], [], [], []]);
  });

  test("listPartsAll sends the sort and the filter with every page", async () => {
    const client = sdk();
    const filter = { quantity: { gte: 2 }, discontinued: { in: [true, false] } };
    expect(await collect(client.catalog.listPartsAll({ sort: "-quantity", filter, limit: 2 }))).toEqual([
      "eyelet",
      "dowel",
      "clamp",
      "bracket",
    ]);
    const filters = [
      ["filter[quantity][gte]", "2"],
      ["filter[discontinued][in]", "true,false"],
    ];
    expect(queries()).toEqual([
      [["sort", "-quantity"], ["limit", "2"], ...filters],
      [["sort", "-quantity"], ["limit", "2"], ["cursor", sortedCursorOf("-quantity", 2)], ...filters],
    ]);
    // The caller's filter is sent as it was given, not changed by a page.
    expect(filter).toEqual({ quantity: { gte: 2 }, discontinued: { in: [true, false] } });

    // One page beside a sort and a cursor.
    sdk();
    const page = await client.catalog.listParts({ sort: "quantity", cursor: sortedCursorOf("quantity", 1), filter: { kind: "bolt" } });
    expect(page.items.map((part) => part.name)).toEqual(["dowel"]);
    expect(queries()).toEqual([
      [
        ["sort", "quantity"],
        ["cursor", sortedCursorOf("quantity", 1)],
        ["filter[kind]", "bolt"],
      ],
    ]);
  });

  test("a filter alone, under another argument's name, travels as filter", async () => {
    const client = sdk();
    const found = await client.catalog.findParts({ where: { name: { contains: "CK" }, supplierId: supplier } });
    expect(found.map((part) => part.name)).toEqual(["bracket"]);
    expect(await client.catalog.findParts()).toHaveLength(5);
    expect(state.requests.every((request) => request.path === "/api/parts/all")).toBe(true);
    expect(queries()).toEqual([
      [
        ["filter[name][contains]", "CK"],
        ["filter[supplierId]", supplier],
      ],
      [],
    ]);
  });

  test("an operator the SDK does not know is sent for the route to refuse", async () => {
    const client = sdk();
    let failure;
    try {
      await client.catalog.listParts({ filter: { name: { like: "a%" } } });
    } catch (err) {
      failure = err;
    }
    expect(failure).toBeInstanceOf(ApiError);
    expect(failure.statusCode).toBe(400);
    expect(failure.message).toBe("Invalid query parameter filter[name]: like is no operator");
    expect(queries()).toEqual([[["filter[name][like]", "a%"]]]);
  });

  test("a filter the route would not read as it was given is refused before the request", async () => {
    const client = sdk();
    const pattern = (path) => [{ validator: "pattern", message: `${path} must not contain a comma or surrounding space.` }];
    const required = (path) => [{ validator: "required", message: `${path} is required.` }];

    // An in's value that holds a comma would be read as two, one with
    // surrounding space trimmed, and a blank one dropped: every one is
    // refused at its index, in one error.
    expect(
      await validationErrors(client.catalog.listParts({ filter: { name: { in: ["axle", "a,b", " c", "d\t", "", "  "] } } }))
    ).toEqual({
      "filter[name][in][1]": pattern("filter[name][in][1]"),
      "filter[name][in][2]": pattern("filter[name][in][2]"),
      "filter[name][in][3]": pattern("filter[name][in][3]"),
      "filter[name][in][4]": required("filter[name][in][4]"),
      "filter[name][in][5]": required("filter[name][in][5]"),
    });

    // More values than an in may list.
    const values = (count) => Array.from({ length: count }, (_, index) => index);
    expect(await validationErrors(client.catalog.listParts({ filter: { quantity: { in: values(101) } } }))).toEqual({
      "filter[quantity][in]": [{ validator: "listMax", message: "filter[quantity][in] must contain at most 100 items." }],
    });

    // A contains of no text.
    expect(await validationErrors(client.catalog.listParts({ filter: { name: { contains: "" } } }))).toEqual({
      "filter[name][contains]": [{ validator: "minLength", message: "filter[name][contains] needs text." }],
    });

    // More filters than a request may hold.
    const fields = (count) => Object.fromEntries(values(count).map((index) => [`f${index}`, index]));
    expect(await validationErrors(client.catalog.listParts({ filter: fields(51) }))).toEqual({
      filter: [{ validator: "max", message: "filter must hold at most 50 filters." }],
    });

    // Each fault of one filter is named, whatever the argument is called,
    // and the iterator and invokeTool refuse as the method does.
    expect(
      await validationErrors(client.catalog.findParts({ where: { kind: { in: ["bolt", "nut,washer"] }, name: { contains: "" } } }))
    ).toEqual({
      "filter[kind][in][1]": pattern("filter[kind][in][1]"),
      "filter[name][contains]": [{ validator: "minLength", message: "filter[name][contains] needs text." }],
    });
    expect(await validationErrors(collect(client.catalog.listPartsAll({ filter: { id: { in: [" "] } } })))).toEqual({
      "filter[id][in][0]": required("filter[id][in][0]"),
    });
    expect(
      await validationErrors(invokeTool(client, "catalog.listParts", { filter: { supplierId: { in: [`${supplier},${supplier}`] } } }))
    ).toEqual({ "filter[supplierId][in][0]": pattern("filter[supplierId][in][0]") });
    expect(state.requests).toEqual([]);

    // The most an in and a request may hold are sent, a comma or a space
    // in any other operator's value travels as it is, and an in of no
    // value is left out.
    expect((await client.catalog.listParts({ filter: { quantity: { in: values(100) } }, limit: 5 })).items).toHaveLength(5);
    expect(await client.catalog.findParts({ where: fields(50) })).toEqual([]);
    expect(await client.catalog.findParts({ where: { name: { eq: " a, b ", contains: " , ", in: [] } } })).toEqual([]);
    expect(queries().map((query) => query.length)).toEqual([2, 50, 2]);
    expect(queries()[2]).toEqual([
      ["filter[name]", " a, b "],
      ["filter[name][contains]", " , "],
    ]);
  });

  test("invokeTool passes the filter", async () => {
    const client = sdk();
    const page = await invokeTool(client, "catalog.listParts", {
      sort: "-quantity",
      filter: { kind: "nut", quantity: { gt: 1 } },
      limit: 1,
    });
    expect(page.items.map((part) => part.name)).toEqual(["eyelet"]);
    const next = await invokeTool(client, "catalog.listParts", {
      sort: "-quantity",
      filter: { kind: "nut", quantity: { gt: 1 } },
      cursor: page.next,
      limit: 1,
    });
    expect(next.items.map((part) => part.name)).toEqual(["bracket"]);
    const found = await invokeTool(client, "catalog.findParts", { where: { retiredOn: { isNull: false } } });
    expect(found.map((part) => part.name)).toEqual(["bracket", "dowel"]);
    const filters = [
      ["filter[kind]", "nut"],
      ["filter[quantity][gt]", "1"],
    ];
    expect(queries()).toEqual([
      [["sort", "-quantity"], ["limit", "1"], ...filters],
      [["sort", "-quantity"], ["cursor", sortedCursorOf("-quantity", 1)], ["limit", "1"], ...filters],
      [["filter[retiredOn][isNull]", "false"]],
    ]);
  });

  test("the filter writer sends filter_parity.json's queries", async () => {
    const vectors = JSON.parse(await Bun.file(filterParity).text()).queries;
    expect(vectors.length).toBeGreaterThan(0);
    for (const vector of vectors) {
      // The typed filter the vector's filters make: a member per field,
      // holding each of its operators.
      const filter = {};
      for (const { field, operator, values } of vector.filters) {
        filter[field] ??= {};
        filter[field][operator] = operator === "in" ? values : operator === "isNull" ? values[0] === "true" : values[0];
      }
      expect([vector.name, Object.entries(filterParams(filter))]).toEqual([vector.name, vector.pairs]);
      // A request carries the same pairs.
      const client = sdk();
      await client.catalog.findParts({ where: filter });
      expect([vector.name, ...queries()]).toEqual([vector.name, vector.pairs]);
      // A value alone is the equality the vector writes.
      if (vector.filters.length === 1 && vector.filters[0].operator === "eq") {
        const { field, values } = vector.filters[0];
        expect(Object.entries(filterParams({ [field]: values[0] }))).toEqual(vector.pairs);
      }
    }
  });
});
