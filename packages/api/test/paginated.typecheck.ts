// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @paginated takes no argument or an object of defaultLimit and
// maxLimit, both optional numbers, on a method.
import { HttpMethod, paginated, rest } from "@superschematic/api";

export class Checked {
  @rest(HttpMethod.GET, "items")
  @paginated()
  listItems(): string[] {
    return [];
  }

  @rest(HttpMethod.GET, "tags")
  @paginated({ defaultLimit: 2, maxLimit: 5 })
  listTags(): string[] {
    return [];
  }

  @rest(HttpMethod.GET, "labels")
  @paginated({ maxLimit: 10 })
  listLabels(): string[] {
    return [];
  }

  // @ts-expect-error a limit is a number
  @paginated({ maxLimit: "10" })
  byText(): string[] {
    return [];
  }

  // @ts-expect-error an unknown key
  @paginated({ pageSize: 10 })
  unknownKey(): string[] {
    return [];
  }
}
